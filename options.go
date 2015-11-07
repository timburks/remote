package remote

import (
	"github.com/docopt/docopt-go"
	"strings"
)

type Command struct {
	M map[string]interface{}
}

func (command *Command) Is(c string) bool {
	hasAll := true
	terms := strings.Split(c, " ")
	for _, term := range terms {
		if !command.M[term].(bool) {
			hasAll = false
		}
	}
	return hasAll
}

func NewCommand(usage string, name string) (command *Command, err error) {

	arguments, err := docopt.Parse(usage, nil, true, "tool", false)
	if err != nil {
		return
	}
	command = &Command{arguments}
	return
}
