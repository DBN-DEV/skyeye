package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/DBN-DEV/skyeye/agent"
	"github.com/DBN-DEV/skyeye/version"
)

type option struct {
	target string
}

func (o *option) validate() error {
	if o.target == "" {
		return errors.New("--target option is required")
	}

	return nil
}

func (o *option) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.target, "target", "", "management server target address")
}

func (o *option) run() error {
	manager, err := agent.NewManager(o.target)
	if err != nil {
		return fmt.Errorf("could not create agent manager: %w", err)
	}

	return manager.Run()
}

func NewCmd() *cobra.Command {
	var opt option

	cmd := &cobra.Command{
		Version: version.Version,
		Use:     "skyeye-agent",
		Short:   "Skyeye Agent",
		Long:    "Skyeye Agent connects to the management server and executes probe tasks.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opt.validate(); err != nil {
				return err
			}

			err := opt.run()
			cobra.CheckErr(err)

			return nil
		},
	}

	opt.addFlags(cmd)

	return cmd
}

func main() {
	version.Println()

	cmd := NewCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Println("Error:", err)
	}
}
