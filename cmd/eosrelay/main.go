package main

import (
	"os"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/kingblockio/relay/app"
	"github.com/cosmos/cosmos-sdk/version"
	ibccmd "github.com/cosmos/cosmos-sdk/x/ibc/client/cli"
	"github.com/spf13/cobra"
	"github.com/tendermint/tendermint/libs/cli"
)

// rootCmd is the entry point for this binary
var (
	rootCmd = &cobra.Command{
		Use:   "relay",
		Short: "king chain and hub communication relay",
	}
)

func main() {
	// disable sorting
	cobra.EnableCommandSorting = false

	// get the codec
	cdc := app.MakeCodec()

	// TODO: Setup keybase, viper object, etc. to be passed into
	// the below functions and eliminate global vars, like we do
	// with the cdc.


	rootCmd.AddCommand(
		client.PostCommands(
			ibccmd.IBCRelayCmd(cdc),
		)...)

	// add proxy, version and key info
	rootCmd.AddCommand(
		client.LineBreak,
		version.VersionCmd,
	)

	// prepare and add flags
	executor := cli.PrepareMainCmd(rootCmd, "BC", os.ExpandEnv("$HOME/.eosrelay"))
	err := executor.Execute()
	if err != nil {
		// Note: Handle with #870
		panic(err)
	}
}
