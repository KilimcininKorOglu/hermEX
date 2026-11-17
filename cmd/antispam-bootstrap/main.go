// Command antispam-bootstrap trains a Bayesian spam model from a labeled corpus
// and writes it as JSON. Point it at directories of spam and ham message files
// (e.g. the SpamAssassin public corpus) to produce the model hermEX embeds as its
// cold-start floor, or that an operator drops into data_dir as antispam-model.json.
package main

import (
	"flag"
	"log"
	"os"

	"hermex/internal/antispam"
	"hermex/internal/atomicfile"
)

func main() {
	spamDir := flag.String("spam", "", "directory of spam message files")
	hamDir := flag.String("ham", "", "directory of ham (non-spam) message files")
	out := flag.String("out", "", "output model JSON path (default stdout)")
	flag.Parse()

	if *spamDir == "" || *hamDir == "" {
		log.Fatal("antispam-bootstrap: both -spam and -ham directories are required")
	}

	model := antispam.NewBayesModel()
	ns, err := antispam.TrainFromDir(model, *spamDir, true)
	if err != nil {
		log.Fatalf("antispam-bootstrap: spam corpus: %v", err)
	}
	nh, err := antispam.TrainFromDir(model, *hamDir, false)
	if err != nil {
		log.Fatalf("antispam-bootstrap: ham corpus: %v", err)
	}

	// The model file keeps the mode os.Create gave it before (0644 under the usual
	// umask), because another process loads it.
	save := func() error { return model.Save(os.Stdout) }
	if *out != "" {
		save = func() error { return atomicfile.WriteFunc(*out, 0o644, model.Save) }
	}
	if err := save(); err != nil {
		log.Fatalf("antispam-bootstrap: write model: %v", err)
	}
	log.Printf("antispam-bootstrap: trained on %d spam + %d ham messages", ns, nh)
}
