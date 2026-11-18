package activesync

import (
	"errors"
	"fmt"
	"net/http"

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
func withOriginal(mailbox string, device []byte, src *replySource) ([]byte, error) {
	st, err := objectstore.OpenExisting(mailbox)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
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
	oxcmail.AppendOriginal(reply, orig, src.forward)
	return oxcmail.Export(reply, oxcmail.Options{})
}
