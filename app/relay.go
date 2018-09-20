package cli

import (
	"os"
	"time"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	wire "github.com/cosmos/cosmos-sdk/wire"
	authcmd "github.com/cosmos/cosmos-sdk/x/auth/client/cli"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/cosmos/cosmos-sdk/x/ibc"
	abci "github.com/tendermint/tendermint/abci/types"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

const (
	FlagFromChainID     = "from-chain-id"
	FlagFromChainNode   = "from-chain-node"
)

type relayCommander struct {
	cdc       *wire.Codec
	//address   sdk.AccAddress
	decoder   auth.AccountDecoder
	mainStore string
	ibcStore  string
	accStore  string

	logger log.Logger
}

func MakeCodec() *wire.Codec {
	cdc := wire.NewCodec()

	wire.RegisterCrypto(cdc)
	sdk.RegisterWire(cdc)
	bank.RegisterWire(cdc)
	ibc.RegisterWire(cdc)
    auth.RegisterWire(cdc)

	// register custom types
	cdc.RegisterConcrete(&types.AppAccount{}, "kingchain/Account", nil)

	cdc.Seal()

	return cdc
}

// IBC relay command
func IBCRelayCmd(cdc *wire.Codec) *cobra.Command {
	cmdr := relayCommander{
		cdc:       cdc,
		decoder:   authcmd.GetAccountDecoder(cdc),
		ibcStore:  "ibc",
		mainStore: "main",
		accStore:  "acc",

		logger: log.NewTMLogger(log.NewSyncWriter(os.Stdout)),
	}

	cmd := &cobra.Command{
		Use: "relay",
		Run: cmdr.runIBCRelay,
	}

	cmd.Flags().String(FlagFromChainID, "", "Chain ID for ibc node to check outgoing packets")
	cmd.Flags().String(FlagFromChainNode, "tcp://localhost:26657", "<host>:<port> to tendermint rpc interface for this chain")

	cmd.MarkFlagRequired(FlagFromChainID)
	cmd.MarkFlagRequired(FlagFromChainNode)

	viper.BindPFlag(FlagFromChainID, cmd.Flags().Lookup(FlagFromChainID))
	viper.BindPFlag(FlagFromChainNode, cmd.Flags().Lookup(FlagFromChainNode))

	return cmd
}

// nolint: unparam
func (c relayCommander) runIBCRelay(cmd *cobra.Command, args []string) {
	fromChainID := viper.GetString(FlagFromChainID)
	fromChainNode := viper.GetString(FlagFromChainNode)
	toChainID := viper.GetString(client.FlagChainID)
	toChainNode := viper.GetString(client.FlagNode)

	c.loop(fromChainID, fromChainNode, toChainID, toChainNode)
}

// This is nolinted as someone is in the process of refactoring this to remove the goto
// nolint: gocyclo
func (c relayCommander) loop(fromChainID, fromChainNode, toChainID, toChainNode string) {

	ctx := context.NewCoreContextFromViper()
	// get password
	passphrase, err := ctx.GetPassphraseFromStdin(ctx.FromAddressName)
	if err != nil {
		panic(err)
	}

	ingressSequenceKey := ibc.IngressSequenceKey(fromChainID)
	egressSequenceKey := ibc.EgressSequenceKey(toChainID)

OUTER:
	for {
		time.Sleep(10 * time.Second)

		ingressSequencebz, err := query(toChainNode, ingressSequenceKey, c.ibcStore)
		if err != nil {
			panic(err)
		}

		var ingressSequence int64
		if ingressSequencebz == nil {
			ingressSequence = 0
		} else if err = c.cdc.UnmarshalBinary(ingressSequencebz, &ingressSequence); err != nil {
			panic(err)
		}
		
		log := fmt.Sprintf("query chain : %s, ingress : %s, number : %d", toChainID, ingressSequenceKey, ingressSequence)
		c.logger.Info("log", "string", log)

		egressSequencebz, err := query(fromChainNode, egressSequenceKey, c.ibcStore)
		if err != nil {
			c.logger.Error("error querying outgoing packet list length", "err", err)
			continue OUTER //TODO replace with continue (I think it should just to the correct place where OUTER is now)
		}
		var egressSequence int64
		if egressSequencebz == nil {
			egressSequence = 0
		} else if err = c.cdc.UnmarshalBinary(egressSequencebz, &egressSequence); err != nil {
			panic(err)
		}
		
		log = fmt.Sprintf("query chain : %s, egress : %s, number : %d", fromChainID, egressSequenceKey, egressSequence)
		c.logger.Info("log", "string", log)	
		
		if egressSequence > ingressSequence {
			c.logger.Info("Detected IBC packet", "total", egressSequence - ingressSequence)
		}

		seq := (c.getSequence(toChainNode))
		//c.logger.Info("broadcast tx seq", "number", seq)

		for i := ingressSequence; i < egressSequence; i++ {
			res, err := queryWithProof(fromChainNode, ibc.EgressKey(toChainID, i), c.ibcStore)
			if err != nil {
				c.logger.Error("error querying egress packet", "err", err)
				continue OUTER // TODO replace to break, will break first loop then send back to the beginning (aka OUTER)
			}
			
			// get the from address
			from, err := ctx.GetFromAddress()
			if err != nil {
				panic(err) 
			}
			
			commit_res, err := commitWithProof(fromChainNode, res.Height + 1, c.ibcStore)
			commit_update := ibc.CommitUpdate{
				Header: *commit_res.Header,
				Commit: *commit_res.Commit,
			}
			bz, err := c.cdc.MarshalBinary(commit_update)
			if err != nil {
				panic(err)
			}
		
			commit_msg := ibc.IBCRelayMsg{
				SrcChain: fromChainID,
				DestChain: toChainID,
				Relayer: from,
				Payload: bz,
				MsgType: ibc.COMMITUPDATE,
			}
			
			new_ctx := context.NewCoreContextFromViper().WithDecoder(authcmd.GetAccountDecoder(c.cdc)).WithSequence(i)
			//ctx = ctx.WithNodeURI(viper.GetString(FlagHubChainNode))
			//ctx := context.NewCoreContextFromViper().WithDecoder(authcmd.GetAccountDecoder(c.cdc))
			new_ctx, err = new_ctx.Ensure(ctx.FromAddressName)
			new_ctx = new_ctx.WithSequence(seq)
			xx_res, err := new_ctx.SignAndBuild(ctx.FromAddressName, passphrase, []sdk.Msg{commit_msg}, c.cdc)
			if err != nil {
				panic(err)
			}

			//
			c.logger.Info("broadcast tx, type : commitupdate, sequence : ", "int", seq)
			
			//
			err = c.broadcastTx(seq, toChainNode, xx_res)
			seq++
			if err != nil {
				c.logger.Error("error broadcasting ingress packet", "err", err)
				continue OUTER // TODO replace to break, will break first loop then send back to the beginning (aka OUTER)
			}

            new_ibcpacket := ibc.NewIBCPacket {
            	Packet: res.Value,
            	Proof: res.Proof,
            	Sequence: i,
            }
            
            bz, err = c.cdc.MarshalBinary(new_ibcpacket)
			if err != nil {
				panic(err)
			}
			
			//
			msg := ibc.IBCRelayMsg{
				SrcChain: fromChainID,
				DestChain: toChainID,
				Relayer: from,
				Payload: bz,
				MsgType: ibc.NEWIBCPACKET,
			}
			
			new_ctx = context.NewCoreContextFromViper().WithDecoder(authcmd.GetAccountDecoder(c.cdc)).WithSequence(i)
			//ctx = ctx.WithNodeURI(viper.GetString(FlagHubChainNode))
			//ctx := context.NewCoreContextFromViper().WithDecoder(authcmd.GetAccountDecoder(c.cdc))
			new_ctx, err = new_ctx.Ensure(ctx.FromAddressName)
			new_ctx = new_ctx.WithSequence(seq)
			xx_res, err = new_ctx.SignAndBuild(ctx.FromAddressName, passphrase, []sdk.Msg{msg}, c.cdc)
			if err != nil {
				panic(err)
			}

			//
			c.logger.Info("broadcast tx, type : newibcpacket, sequence : ", "int", seq)
			
			//
			err = c.broadcastTx(seq, toChainNode, xx_res)
			seq++
			if err != nil {
				c.logger.Error("error broadcasting ingress packet", "err", err)
				continue OUTER // TODO replace to break, will break first loop then send back to the beginning (aka OUTER)
			}

			c.logger.Info("Relayed IBC packet", "index", i)
		}
	}
}

func (c relayCommander) getaddress() (addr sdk.AccAddress) {
	address, err := context.NewCoreContextFromViper().GetFromAddress()
	if err != nil {
		panic(err)
	}
	return address
}

func query(node string, key []byte, storeName string) (res []byte, err error) {
	return context.NewCoreContextFromViper().WithNodeURI(node).QueryStore(key, storeName)
}

func queryWithProof(node string, key []byte, storeName string) (x abci.ResponseQuery, err error) {
	return context.NewCoreContextFromViper().WithNodeURI(node).QueryStoreWithProof(key, storeName)
}

func commitWithProof(node string, height int64, storeName string) (x *ctypes.ResultCommit, err error) {
	return context.NewCoreContextFromViper().WithNodeURI(node).CommitWithProof(storeName, height)
}

func (c relayCommander) broadcastTx(seq int64, node string, tx []byte) error {
	_, err := context.NewCoreContextFromViper().WithNodeURI(node).WithSequence(seq + 1).BroadcastTx(tx)
	return err
}

func (c relayCommander) getSequence(node string) int64 {
	res, err := query(node, auth.AddressStoreKey(c.getaddress()), c.accStore)
	if err != nil {
		panic(err)
	}
	if nil != res {
		account, err := c.decoder(res)
		if err != nil {
			panic(err)
		}

		return account.GetSequence()
	}

	return 0
}
