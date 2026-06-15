package rpchttp

import (
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// RTS (RPC-over-HTTP tunnelling) command types ([MS-RPCH] 2.2.3.5.1).
const (
	rtsReceiveWindowSize    uint32 = 0
	rtsFlowControlAck       uint32 = 1
	rtsConnectionTimeout    uint32 = 2
	rtsCookie               uint32 = 3
	rtsChannelLifetime      uint32 = 4
	rtsClientKeepalive      uint32 = 5
	rtsVersion              uint32 = 6
	rtsEmpty                uint32 = 7
	rtsPadding              uint32 = 8
	rtsNegativeANCE         uint32 = 9
	rtsANCE                 uint32 = 10
	rtsClientAddress        uint32 = 11
	rtsAssociationGroupID   uint32 = 12
	rtsDestination          uint32 = 13
	rtsPingTrafficSentNotif uint32 = 14
)

// RTS pfc-style flags carried in the RTS PDU body ([MS-RPCH] 2.2.3.4).
const (
	rtsFlagNone           uint16 = 0
	rtsFlagPing           uint16 = 1 << 0
	rtsFlagOtherCmd       uint16 = 1 << 1
	rtsFlagRecycleChannel uint16 = 1 << 2
	rtsFlagInChannel      uint16 = 1 << 3
	rtsFlagOutChannel     uint16 = 1 << 4
	rtsFlagEOF            uint16 = 1 << 5
	rtsFlagEcho           uint16 = 1 << 6
)

// connectionTimeoutMS is the connection timeout advertised in CONN/A3 and
// CONN/C2 ([MS-RPCH]); a generous value so a kept-open channel is not reaped.
const connectionTimeoutMS uint32 = 14400000 // 4 hours

// fdClient is the DESTINATION value naming the client side ([MS-RPCH]).
const fdClient uint32 = 0

// rtsCommand is one decoded RTS command: its type plus whichever payload that
// type carries (a uint32 for the scalar commands, a GUID for cookie/group-id).
type rtsCommand struct {
	Type uint32
	U32  uint32
	GUID mapi.GUID
}

// parseRTS decodes an RTS PDU (the 16-byte header is skipped): the RTS flags and
// the command list. Command bodies are sized by type; an unrecognised type stops
// the parse rather than guessing a length.
func parseRTS(pdu []byte) (flags uint16, cmds []rtsCommand, err error) {
	p := ndr.NewPull(pdu)
	if _, err = p.Raw(16); err != nil { // skip the NCACN header
		return 0, nil, err
	}
	if flags, err = p.Uint16(); err != nil {
		return 0, nil, err
	}
	num, err := p.Uint16()
	if err != nil {
		return flags, nil, err
	}
	for range num {
		var c rtsCommand
		if c.Type, err = p.Uint32(); err != nil {
			return flags, cmds, err
		}
		switch c.Type {
		case rtsCookie, rtsAssociationGroupID:
			if c.GUID, err = p.GUID(); err != nil {
				return flags, cmds, err
			}
		case rtsReceiveWindowSize, rtsConnectionTimeout, rtsChannelLifetime,
			rtsClientKeepalive, rtsVersion, rtsDestination, rtsPingTrafficSentNotif:
			if c.U32, err = p.Uint32(); err != nil {
				return flags, cmds, err
			}
		case rtsFlowControlAck: // bytes_received + available_window + channel_cookie
			if _, err = p.Uint32(); err != nil {
				return flags, cmds, err
			}
			if _, err = p.Uint32(); err != nil {
				return flags, cmds, err
			}
			if _, err = p.GUID(); err != nil {
				return flags, cmds, err
			}
		case rtsEmpty, rtsANCE, rtsNegativeANCE:
			// no body
		case rtsPadding:
			n, perr := p.Uint32()
			if perr != nil {
				return flags, cmds, perr
			}
			if _, err = p.Raw(int(n)); err != nil {
				return flags, cmds, err
			}
		default:
			return flags, cmds, ndr.ErrFormat
		}
		cmds = append(cmds, c)
	}
	return flags, cmds, nil
}

// cookies returns the COOKIE command GUIDs in order. CONN/A1 and CONN/B1 both
// carry the connection cookie first and the channel cookie second.
func cookies(cmds []rtsCommand) []mapi.GUID {
	var out []mapi.GUID
	for _, c := range cmds {
		if c.Type == rtsCookie {
			out = append(out, c.GUID)
		}
	}
	return out
}

// receiveWindowSize returns the RECEIVE_WINDOW_SIZE command value (the OUT
// channel's window from CONN/A1), or a default when absent.
func receiveWindowSize(cmds []rtsCommand) uint32 {
	for _, c := range cmds {
		if c.Type == rtsReceiveWindowSize {
			return c.U32
		}
	}
	return 0x10000 // 64 KiB default
}

// buildRTSBody marshals an RTS PDU body: flags, command count, then the
// commands. Each command is a 4-byte type followed by its (4-byte-aligned)
// payload, matching the connection-oriented RTS wire form.
func buildRTSBody(flags uint16, cmds []rtsCommand) []byte {
	p := ndr.NewPush()
	p.Uint16(flags)
	p.Uint16(uint16(len(cmds)))
	for _, c := range cmds {
		p.Uint32(c.Type)
		switch c.Type {
		case rtsCookie, rtsAssociationGroupID:
			p.GUID(c.GUID)
		default:
			p.Uint32(c.U32)
		}
	}
	p.Align(4) // trailer alignment
	return p.Bytes()
}

// buildConnA3 builds the server's CONN/A3 reply to CONN/A1: a single
// CONNECTION_TIMEOUT command. It echoes the client's call id.
func buildConnA3(callID uint32) []byte {
	body := buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: rtsConnectionTimeout, U32: connectionTimeoutMS},
	})
	return ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, callID, body)
}

// buildConnC2 builds the "virtual connection established" CONN/C2 PDU, emitted
// on the OUT channel once both channels are present: VERSION, the negotiated
// receive window, and the connection timeout.
func buildConnC2(callID, windowSize uint32) []byte {
	body := buildRTSBody(rtsFlagNone, []rtsCommand{
		{Type: rtsVersion, U32: 1},
		{Type: rtsReceiveWindowSize, U32: windowSize},
		{Type: rtsConnectionTimeout, U32: connectionTimeoutMS},
	})
	return ndr.Frame(ndr.PktRTS, ndr.PfcFirstFrag|ndr.PfcLastFrag, callID, body)
}
