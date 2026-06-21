package admin

import (
	"fmt"

	"hermex/internal/antispam"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// retrainSampleCap bounds how many of a folder's most recent messages each
// mailbox contributes to a retrain, so a very large inbox cannot make the job run
// unbounded.
const retrainSampleCap = 500

// performBayesRetrain rebuilds the Bayesian spam model from every mailbox — the
// Junk folder as spam, the inbox as ham — and writes it atomically to the path the
// MTA loads at startup. It is the handler for the "bayes-retrain" task. A mailbox
// that fails to open is skipped, so one bad store cannot fail the whole retrain.
func (s *Server) performBayesRetrain() (string, error) {
	dirs, err := s.dir.Maildirs()
	if err != nil {
		return "", err
	}
	model := antispam.NewBayesModel()
	var nspam, nham, nbox int
	for _, dir := range dirs {
		st, err := objectstore.Open(dir)
		if err != nil {
			continue
		}
		nspam += trainFolder(st, model, int64(mapi.PrivateFIDJunk), true)
		nham += trainFolder(st, model, int64(mapi.PrivateFIDInbox), false)
		st.Close()
		nbox++
	}
	if err := model.SaveFile(s.paths.AntispamModelPath()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Retrained on %d spam + %d ham messages from %d mailboxes.", nspam, nham, nbox), nil
}

// trainFolder trains the model on up to retrainSampleCap of a folder's most recent
// messages with the given label, returning the number trained.
func trainFolder(st *objectstore.Store, m *antispam.BayesModel, folder int64, spam bool) int {
	msgs, err := st.ListMessages(folder)
	if err != nil {
		return 0
	}
	if len(msgs) > retrainSampleCap {
		msgs = msgs[len(msgs)-retrainSampleCap:]
	}
	n := 0
	for _, mi := range msgs {
		raw, err := st.GetMessageRaw(folder, mi.UID)
		if err != nil {
			continue
		}
		m.Train(antispam.MessageText(raw), spam)
		n++
	}
	return n
}
