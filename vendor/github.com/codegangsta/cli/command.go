package cli

import (
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
)

// Command is a subcommand for a cli.App.
type Command struct {
	// The name of the command
	Name string
	// short name of the command. Typically one character (deprecated, use `Aliases`)
	ShortName string
	// A list of aliases for the command
	Aliases []string
	// A short description of the usage of this command
	Usage string
	// Custom text to show on USAGE section of help
	UsageText string
	// A longer explanation of how the command works
	Description string
	// A short description of the arguments of this command
	ArgsUsage string
	// The category the command is part of
	Category string
	// The function to call when checking for bash command completions
	BashComplete BashCompleteFunc
	// An action to execute before any sub-subcommands are run, but after the context is ready
	// If a non-nil error is returned, no sub-subcommands are run
	Before BeforeFunc
	// An action to execute after any subcommands are run, but after the subcommand has finished
	// It is run even if Action() panics
	After AfterFunc
	// The function to call when this command is invoked
	Action interface{}
	// TODO: replace `Action: interface{}` with `Action: ActionFunc` once some kind
	// of deprecation period has passed, maybe?

	// Execute this function if a usage error occurs.
	OnUsageError OnUsageErrorFunc
	// List of child commands
	Subcommands Commands
	// List of flags to parse
	Flags []Flag
	// Treat all flags as normal arguments if true
	SkipFlagParsing bool
	// Skip argument reordering which attempts to move flags before arguments,
	// but only works if all flags appear after all arguments. This behavior was
	// removed n version 2 since it only works under specific conditions so we
	// backport here by exposing it as an option for compatibility.
	SkipArgReorder bool
	// Boolean to hide built-in help command
	HideHelp bool
	// Boolean to hide this command from help or completion
	Hidden bool
	// Boolean to enable short-option handling so user can combine several
	// single-character bool arguements into one
	// i.e. foobar -o -v -> foobar -ov
	UseShortOptionHandling bool

	// Full name of command for help, defaults to full command name, including parent commands.
	HelpName        string
	commandNamePath []string

	// CustomHelpTemplate the text template for the command help topic.
	// cli.go uses text/template to render templates. You can
	// render custom help text by setting this variable.
	CustomHelpTemplate string
}

type CommandsByName []Command

func (c CommandsByName) Len() int {
	return len(c)
}

func (c CommandsByName) Less(i, j int) bool {
	return lexicographicLess(c[i].Name, c[j].Name)
}

func (c CommandsByName) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}

// FullName returns the full name of the command.
// For subcommands this ensures that parent commands are part of the command path
func (c Command) FullName() string {
	if c.commandNamePath == nil {
		return c.Name
	}
	return strings.Join(c.commandNamePath, " ")
}

// Commands is a slice of Command
type Commands []Command

// Run invokes the command given the context, parses ctx.Args() to generate command-specific flags
func (c Command) Run(ctx *Context) (err error) {
	if len(c.Subcommands) > 0 {
		return c.startApp(ctx)
	}

	if !c.HideHelp && (HelpFlag != BoolFlag{}) {
		// append help to flags
		c.Flags = append(
			c.Flags,
			HelpFlag,
		)
	}

	set, err := flagSet(c.Name, c.Flags)
	if err != nil {
		return err
	}
	set.SetOutput(ioutil.Discard)
	firstFlagIndex, terminatorIndex := getIndexes(ctx)
	flagArgs, regularArgs := getAllArgs(ctx.Args(), firstFlagIndex, terminatorIndex)
	if c.UseShortOptionHandling {
		flagArgs = translateShortOptions(flagArgs)
	}
	if c.SkipFlagParsing {
		err = set.Parse(append([]string{"--"}, ctx.Args().Tail()...))
	} else if !c.SkipArgReorder {
		if firstFlagIndex > -1 {
			err = set.Parse(append(flagArgs, regularArgs...))
		} else {
			err = set.Parse(ctx.Args().Tail())
		}
	} else if c.UseShortOptionHandling {
		if terminatorIndex == -1 && firstFlagIndex > -1 {
			// Handle shortname AND no options
			err = set.Parse(append(regularArgs, flagArgs...))
		} else {
			// Handle shortname and options
			err = set.Parse(flagArgs)
		}
	} else {
		err = set.Parse(append(regularArgs, flagArgs...))
	}

	nerr := normalizeFlags(c.Flags, set)
	if nerr != nil {
		fmt.Fprintln(ctx.App.Writer, nerr)
		fmt.Fprintln(ctx.App.Writer)
		ShowCommandHelp(ctx, c.Name)
		return nerr
	}

	context := NewContext(ctx.App, set, ctx)
	context.Command = c
	if checkCommandCompletions(context, c.Name) {
		return nil
	}

	if err != nil {
		if c.OnUsageError != nil {
			err := c.OnUsageError(context, err, false)
			context.App.handleExitCoder(context, err)
			return err
		}
		fmt.Fprintln(context.App.Writer, "Incorrect Usage:", err.Error())
		fmt.Fprintln(context.App.Writer)
		ShowCommandHelp(context, c.Name)
		return err
	}

	if checkCommandHelp(context, c.Name) {
		return nil
	}

	if c.After != nil {
		defer func() {
			afterErr := c.After(context)
			if afterErr != nil {
				context.App.handleExitCoder(context, err)
				if err != nil {
					err = NewMultiError(err, afterErr)
				} else {
					err = afterErr
				}
			}
		}()
	}

	if c.Before != nil {
		err = c.Before(context)
		if err != nil {
			ShowCommandHelp(context, c.Name)
			context.App.handleExitCoder(context, err)
			return err
		}
	}

	if c.Action == nil {
		c.Action = helpSubcommand.Action
	}

	err = HandleAction(c.Action, context)

	if err != nil {
		context.App.handleExitCoder(context, err)
	}
	return err
}

func getIndexes(ctx *Context) (int, int) {
	firstFlagIndex := -1
	terminatorIndex := -1
	for index, arg := range ctx.Args() {
		if arg == "--" {
			terminatorIndex = index
			break
		} else if arg == "-" {
			// Do nothing. A dash alone is not really a flag.
			continue
		} else if strings.HasPrefix(arg, "-") && firstFlagIndex == -1 {
			firstFlagIndex = index
		}
	}
	if len(ctx.Args()) > 0 && !strings.HasPrefix(ctx.Args()[0], "-") && firstFlagIndex == -1 {
		return -1, -1
	}

	return firstFlagIndex, terminatorIndex

}

// copyStringslice takes a string slice and copies it
func copyStringSlice(slice []string, start, end int) []string {
	newSlice := make([]string, end-start)
	copy(newSlice, slice[start:end])
	return newSlice
}

// getAllArgs extracts and returns two string slices representing
// regularArgs and flagArgs
func getAllArgs(args []string, firstFlagIndex, terminatorIndex int) ([]string, []string) {
	var regularArgs []string
	// if there are no options, the we set the index to 1 manually
	if firstFlagIndex == -1 {
		firstFlagIndex = 1
		regularArgs = copyStringSlice(args, 0, len(args))
	} else {
		regularArgs = copyStringSlice(args, 1, firstFlagIndex)
	}
	var flagArgs []string
	// a flag terminatorIndex was found in the input. we need to collect
	// flagArgs based on it.
	if terminatorIndex > -1 {
		flagArgs = copyStringSlice(args, firstFlagIndex, terminatorIndex)
		additionalRegularArgs := copyStringSlice(args, terminatorIndex, len(args))
		regularArgs = append(regularArgs, additionalRegularArgs...)
		for _, i := range additionalRegularArgs {
			regularArgs = append(regularArgs, i)
		}
	} else {
		flagArgs = args[firstFlagIndex:]
	}
	return flagArgs, regularArgs
}

func translateShortOptions(flagArgs Args) []string {
	// separate combined flags
	var flagArgsSeparated []string
	for _, flagArg := range flagArgs {
		if strings.HasPrefix(flagArg, "-") && strings.HasPrefix(flagArg, "--") == false && len(flagArg) > 2 {
			for _, flagChar := range flagArg[1:] {
				flagArgsSeparated = append(flagArgsSeparated, "-"+string(flagChar))
			}
		} else {
			flagArgsSeparated = append(flagArgsSeparated, flagArg)
		}
	}
	return flagArgsSeparated
}

// Names returns the names including short names and aliases.
func (c Command) Names() []string {
	names := []string{c.Name}

	if c.ShortName != "" {
		names = append(names, c.ShortName)
	}

	return append(names, c.Aliases...)
}

// HasName returns true if Command.Name or Command.ShortName matches given name
func (c Command) HasName(name string) bool {
	for _, n := range c.Names() {
		if n == name {
			return true
		}
	}
	return false
}

func (c Command) startApp(ctx *Context) error {
	app := NewApp()
	app.Metadata = ctx.App.Metadata
	// set the name and usage
	app.Name = fmt.Sprintf("%s %s", ctx.App.Name, c.Name)
	if c.HelpName == "" {
		app.HelpName = c.HelpName
	} else {
		app.HelpName = app.Name
	}

	app.Usage = c.Usage
	app.Description = c.Description
	app.ArgsUsage = c.ArgsUsage

	// set CommandNotFound
	app.CommandNotFound = ctx.App.CommandNotFound
	app.CustomAppHelpTemplate = c.CustomHelpTemplate

	// set the flags and commands
	app.Commands = c.Subcommands
	app.Flags = c.Flags
	app.HideHelp = c.HideHelp

	app.Version = ctx.App.Version
	app.HideVersion = ctx.App.HideVersion
	app.Compiled = ctx.App.Compiled
	app.Author = ctx.App.Author
	app.Email = ctx.App.Email
	app.Writer = ctx.App.Writer
	app.ErrWriter = ctx.App.ErrWriter

	app.categories = CommandCategories{}
	for _, command := range c.Subcommands {
		app.categories = app.categories.AddCommand(command.Category, command)
	}

	sort.Sort(app.categories)

	// bash completion
	app.EnableBashCompletion = ctx.App.EnableBashCompletion
	if c.BashComplete != nil {
		app.BashComplete = c.BashComplete
	}

	// set the actions
	app.Before = c.Before
	app.After = c.After
	if c.Action != nil {
		app.Action = c.Action
	} else {
		app.Action = helpSubcommand.Action
	}
	app.OnUsageError = c.OnUsageError

	for index, cc := range app.Commands {
		app.Commands[index].commandNamePath = []string{c.Name, cc.Name}
	}

	return app.RunAsSubcommand(ctx)
}

// VisibleFlags returns a slice of the Flags with Hidden=false
func (c Command) VisibleFlags() []Flag {
	return visibleFlags(c.Flags)
}
