package cli

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"frostroot/internal/form"
)

// askFieldsPlain asks fields one line at a time through a.Prompt, starting
// from initial. Selects are answered with a number or the value, confirms
// with y or n, multi-selects with numbers or names. Unacceptable answers are
// collected as problems rather than asked again, so scripted input can never
// loop; the caller prints them and writes nothing.
func (a *App) askFieldsPlain(fields []form.Field, initial form.Values) (form.Values, []string, error) {
	values := initial.Clone()
	var problems []string
	currentPage := ""
	for _, field := range fields {
		if field.Page != currentPage {
			currentPage = field.Page
			a.stdoutf("\n%s\n", currentPage)
		}
		var err error
		switch field.Kind {
		case form.KindInput:
			err = a.askInputPlain(field, values, &problems)
		case form.KindSelect:
			err = a.askSelectPlain(field, values, &problems)
		case form.KindConfirm:
			err = a.askConfirmPlain(field, values, &problems)
		case form.KindMultiSelect:
			err = a.askMultiSelectPlain(field, values, &problems)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return values, problems, nil
}

func (a *App) askInputPlain(field form.Field, values form.Values, problems *[]string) error {
	answer, err := a.Prompt.Ask(field.Title, values.String(field.Key))
	if err != nil {
		return err
	}
	answer = strings.TrimSpace(answer)
	values[field.Key] = answer
	if field.Validate != nil {
		if err := field.Validate(answer); err != nil {
			*problems = append(*problems, err.Error())
		}
	}
	return nil
}

// maxListedOptions is how many options the plain interface lists before it
// asks for a value instead: nobody scrolls through 300 timezones.
const maxListedOptions = 24

func (a *App) askSelectPlain(field form.Field, values form.Values, problems *[]string) error {
	question := field.Title + " (number or value)"
	if len(field.Options) > maxListedOptions {
		question = field.Title
		a.stdoutf("  %d choices, for example %s\n", len(field.Options), exampleOptions(field.Options, values.String(field.Key)))
	} else {
		for number, option := range field.Options {
			note := ""
			if option.Description != "" {
				note = "  (" + option.Description + ")"
			}
			a.stdoutf("  %2d) %s%s\n", number+1, option.DisplayLabel(), note)
		}
	}
	answer, err := a.Prompt.Ask(question, values.String(field.Key))
	if err != nil {
		return err
	}
	value, found := resolveOption(field.Options, strings.TrimSpace(answer))
	if !found {
		*problems = append(*problems, fmt.Sprintf("unknown %s %q", strings.ToLower(field.Title), strings.TrimSpace(answer)))
		return nil
	}
	values[field.Key] = value
	return nil
}

func (a *App) askConfirmPlain(field form.Field, values form.Values, problems *[]string) error {
	answer, err := a.askYesNoAnswer(field.Title, values.Bool(field.Key))
	if err != nil {
		return err
	}
	decision, understood := parseYesNo(answer)
	if !understood {
		*problems = append(*problems, fmt.Sprintf("%s: answer y or n, not %q", field.Title, answer))
		return nil
	}
	values[field.Key] = decision
	return nil
}

func (a *App) askMultiSelectPlain(field form.Field, values form.Values, problems *[]string) error {
	// Options carry their category in Description; print it once per group.
	currentGroup := ""
	for number, option := range field.Options {
		if option.Description != currentGroup {
			currentGroup = option.Description
			a.stdoutf("  %s\n", currentGroup)
		}
		a.stdoutf("  %2d) %s\n", number+1, option.DisplayLabel())
	}
	answer, err := a.Prompt.Ask(field.Title+" (numbers or names, separated by spaces or commas)", strings.Join(values.Strings(field.Key), " "))
	if err != nil {
		return err
	}
	var selected []string
	for _, token := range splitAnswers(answer) {
		value, found := resolveOption(field.Options, token)
		if !found {
			*problems = append(*problems, fmt.Sprintf("%s: %q is not one of the options; put other package names in the next answer", field.Title, token))
			continue
		}
		selected = append(selected, value)
	}
	values[field.Key] = selected
	return nil
}

// askYesNo asks a yes/no question outside the field table.
func (a *App) askYesNo(question string, defaultAnswer bool) (bool, error) {
	answer, err := a.askYesNoAnswer(question, defaultAnswer)
	if err != nil {
		return false, err
	}
	decision, understood := parseYesNo(answer)
	if !understood {
		return false, fmt.Errorf("%s: answer y or n, not %q", question, answer)
	}
	return decision, nil
}

// askYesNoAnswer asks a yes/no question and returns the raw answer.
func (a *App) askYesNoAnswer(question string, defaultAnswer bool) (string, error) {
	shownDefault := "n"
	if defaultAnswer {
		shownDefault = "y"
	}
	answer, err := a.Prompt.Ask(question+" (y/n)", shownDefault)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

// exampleOptions names a few options from a long list: the current one and
// some others spread through the list.
func exampleOptions(options []form.Option, current string) string {
	examples := []string{current}
	for _, index := range []int{len(options) / 4, len(options) / 2, 3 * len(options) / 4} {
		if value := options[index].Value; value != current {
			examples = append(examples, value)
		}
	}
	return strings.Join(examples, ", ")
}

// parseYesNo understands y, yes, true, n, no and false, in any case.
func parseYesNo(answer string) (decision, understood bool) {
	switch strings.ToLower(answer) {
	case "y", "yes", "true":
		return true, true
	case "n", "no", "false":
		return false, true
	}
	return false, false
}

// resolveOption finds the option answer names, by 1-based number or by
// value.
func resolveOption(options []form.Option, answer string) (value string, found bool) {
	if number, err := strconv.Atoi(answer); err == nil {
		if number >= 1 && number <= len(options) {
			return options[number-1].Value, true
		}
		return "", false
	}
	for _, option := range options {
		if option.Value == answer {
			return option.Value, true
		}
	}
	return "", false
}

// splitAnswers splits a list answer on commas and whitespace.
func splitAnswers(answer string) []string {
	return strings.FieldsFunc(answer, func(character rune) bool {
		return character == ',' || unicode.IsSpace(character)
	})
}
