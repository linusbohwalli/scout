// Copyright © 2017 Linus Bohwalli <linus.bohwalli@gmail.com>

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"github.com/linusbohwalli/scout"
	"github.com/spf13/cobra"
)


var def bool
// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start",
	Short: "start command starts the scout application to listen to your machines file events",
	Long: `
scout listen to os signals and reports back any events that indicates that a file was written on your server, use the scout api to tell your application to fetch the newly written files.
start command will initiate the scout application
`,
	Run: func(cmd *cobra.Command, args []string) {


		for _, v := range args {
			if v == "default" {
				def = true
			} else {
				def = false
			}
		}

		sct, err := scout.NewScout("")
		if err != nil {
			panic(err)
		}

		sct.Start(def)

	},
}

func init() {
	RootCmd.AddCommand(startCmd)
}
