package activesync

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// prepareSmartResponse resolves the source of a SmartReply or SmartForward and,
// unless the device sent the whole message (ReplaceMime), adds the original to
// the device's MIME. It answers the failure status, or 0 when the request may be
// sent; every failure is decided here, before anything leaves.
func prepareSmartResponse(sess *session, cm *composeMail, r *http.Request) (*replySource, int, error) {
	src, err := findSource(sess, *cm, r)
	if errors.Is(err, errSourceNotFound) {
		return nil, statusItemNotFound, err
	}
	if err != nil {
		return nil, statusMailSubmissionFailed, err
	}
	// A signed or encrypted device message survives the rebuild verbatim: oxcmail
	// keeps it as PrSmimeOriginal and serves those bytes, so nothing is added to it.
	if src == nil || cm.replaceMime {
		return src, 0, nil
	}
	merged, err := withOriginal(sess.mailbox, cm.mime, src)
	if err != nil {
		return nil, statusMessageReplyFailed, err
	}
	cm.mime = merged
	return src, 0, nil
}

// withOriginal rebuilds the device's message with the source appended below its
// text ([MS-ASCMD] 2.2.1.19 and 2.2.1.20: the server retrieves and sends the full
// text of the original). The message goes through oxcmail so the body stays one
// well-formed MIME tree.
func withOriginal(mailbox string, device []byte, src *replySource) (_ []byte, err error) {
	st, err := objectstore.OpenExisting(mailbox)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close the source mailbox: %w", cerr))
		}
	}()
	id := src.objID
	if id == 0 {
		info, err := st.MessageByUID(src.fid, src.uid)
		if err != nil {
			return nil, err
		}
		id = info.ID
	}
	orig, err := st.OpenMessage(id)
	if err != nil {
		return nil, fmt.Errorf("open the source: %w", err)
	}
	reply, err := oxcmail.Import(device, oxcmail.Options{})
	if err != nil {
		return nil, fmt.Errorf("read the device message: %w", err)
	}
	class, _ := orig.Props.Get(mapi.PrMessageClass)
	if s, _ := class.(string); src.forward && strings.HasPrefix(strings.ToLower(s), "ipm.appointment") {
		// An appointment is forwarded as an attachment ([MS-ASCMD] 2.2.1.19).
		if err := oxcmail.AttachOriginal(reply, orig); err != nil {
			return nil, fmt.Errorf("attach the source: %w", err)
		}
	} else {
		oxcmail.AppendOriginal(reply, orig, src.forward)
	}
	return oxcmail.Export(reply, oxcmail.Options{})
}
