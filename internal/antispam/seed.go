package antispam

import (
	"path/filepath"
	"sync"
)

// ModelFileName is the Bayesian model file an operator or the self-training task
// writes under data_dir; it supersedes the embedded floor when present.
const ModelFileName = "antispam-model.json"

// seedSpam and seedHam are a small, curated bootstrap corpus of representative
// spam and legitimate messages. They give the model a cold-start floor before any
// self-training. The floor is deliberately small and conservative — its weight
// alone cannot reach the spam threshold — and an operator can train a far better
// model from a full corpus (cmd/antispam-bootstrap, e.g. the SpamAssassin public
// corpus) into data_dir, which then supersedes this floor.
var seedSpam = []string{
	"Subject: Cheap meds online\n\nBuy cheap viagra and cialis online, no prescription needed, huge discount pharmacy.",
	"Subject: You WON!\n\nCongratulations, you have won the lottery! Claim your prize money now, send your bank details.",
	"Subject: Verify your account\n\nYour account has been suspended. Click here to verify your password immediately or lose access.",
	"Subject: Make money fast\n\nWork from home and earn thousands of dollars per week, guaranteed income, no experience.",
	"Subject: Hot singles\n\nHot singles in your area want to meet you tonight, click now to chat for free.",
	"Subject: Bitcoin investment\n\nDouble your bitcoin in 24 hours, limited time crypto investment opportunity, act now.",
	"Subject: Final notice\n\nUrgent: your payment is overdue, pay now to avoid legal action and extra fees.",
	"Subject: Free gift card\n\nClaim your free $1000 gift card now, just complete this short survey and confirm.",
	"Subject: Weight loss miracle\n\nLose weight fast with this one weird trick, doctors hate it, order pills today.",
	"Subject: Inheritance fund\n\nI am a barrister with an unclaimed inheritance fund, I need your help to transfer millions.",
	"Subject: Refinance now\n\nLowest mortgage rates ever, refinance your loan today and save, no credit check required.",
	"Subject: Account compromised\n\nWe detected suspicious login, confirm your identity and card number to secure your account.",
}

var seedHam = []string{
	"Subject: Meeting tomorrow\n\nHi, can we move our meeting to 3pm tomorrow? I attached the updated agenda for review.",
	"Subject: Project status\n\nHere is the weekly status report for the project, the schedule and milestones are on track.",
	"Subject: Invoice 4821\n\nPlease find attached invoice 4821 for last month, payment terms are net thirty as usual.",
	"Subject: Lunch?\n\nWant to grab lunch later this week? Let me know which day works for you, my treat.",
	"Subject: Code review\n\nI left a few comments on your pull request, mostly minor, looks good to merge after.",
	"Subject: Shipping confirmation\n\nYour order has shipped and will arrive Thursday, here is the tracking number for reference.",
	"Subject: Notes from call\n\nThanks everyone for the call, I summarized the action items and owners below for follow up.",
	"Subject: Vacation request\n\nI would like to take vacation the last week of the month, please let me know if that works.",
	"Subject: Conference talk\n\nMy talk proposal was accepted, I will share the slides draft next week for your feedback.",
	"Subject: Birthday\n\nHappy birthday! Hope you have a wonderful day, let us celebrate this weekend with the team.",
	"Subject: Server maintenance\n\nScheduled maintenance this Saturday night, expect brief downtime, details in the runbook.",
	"Subject: Welcome aboard\n\nWelcome to the team! Your first day is Monday, here is the onboarding checklist and your manager.",
}

var (
	embeddedOnce  sync.Once
	embeddedModel *BayesModel
)

// EmbeddedModel returns the built-in cold-start model trained from the seed
// corpus. It is trained once and cached; the result is read-only.
func EmbeddedModel() *BayesModel {
	embeddedOnce.Do(func() {
		m := NewBayesModel()
		for _, s := range seedSpam {
			m.Train(MessageText([]byte(s)), true)
		}
		for _, h := range seedHam {
			m.Train(MessageText([]byte(h)), false)
		}
		embeddedModel = m
	})
	return embeddedModel
}

// LoadModel returns the model the MTA scores with: the operator- or self-trained
// model at data_dir/antispam-model.json when present, otherwise the embedded
// cold-start floor. A read error falls back to the floor and is returned so the
// caller can log it; the model is always usable (never nil).
func LoadModel(dataDir string) (*BayesModel, error) {
	m, err := LoadModelFile(filepath.Join(dataDir, ModelFileName))
	if err != nil {
		return EmbeddedModel(), err
	}
	if m == nil {
		return EmbeddedModel(), nil
	}
	return m, nil
}
