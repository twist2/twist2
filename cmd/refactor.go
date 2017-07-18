// Copyright 2015 ThoughtWorks, Inc.

// This file is part of Gauge.

// Gauge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Gauge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with Gauge.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"github.com/getgauge/gauge/api"
	"github.com/getgauge/gauge/logger"
	"github.com/getgauge/gauge/refactor"
	"github.com/getgauge/gauge/track"
	"github.com/spf13/cobra"
)

var refactorCmd = &cobra.Command{
	Use:     "refactor [flags] <old step> <new step> [args]",
	Short:   "Refactor steps",
	Long:    `Refactor steps.`,
	Example: `  gauge refactor "old step" "new step"`,
	Run: func(cmd *cobra.Command, args []string) {
		setGlobalFlags()
		if len(args) < 2 {
			logger.Fatalf("Error: Refactor command needs at least two arguments.\n%s", cmd.UsageString())

		}
		if err := isValidGaugeProject(args); err != nil {
			logger.Fatalf(err.Error())
		}
		track.Refactor()
		refactorInit(args)
	},
	DisableAutoGenTag: true,
}

func init() {
	GaugeCmd.AddCommand(refactorCmd)
}

func refactorInit(args []string) {
	startChan := api.StartAPI(false)
	refactor.RefactorSteps(args[0], args[1], startChan, getSpecsDir(args[2:]))
}
