package main

import (
	"errors"
	"fmt"
	"log"

	"hermex/internal/directory"
)

// addHealthTarget stores one Live status target and reports what happened. A
// name already in the table is left as it is and is not an error, so an
// installer can run the command on every install and update; an unusable name
// or URL, or a database failure, is.
func addHealthTarget(add func(directory.HealthTarget) (int64, error), name, url string) (string, error) {
	_, err := add(directory.HealthTarget{Name: name, URL: url})
	switch {
	case err == nil:
		return fmt.Sprintf("health target %q added", name), nil
	case errors.Is(err, directory.ErrHealthTargetExists):
		return fmt.Sprintf("health target %q already present, left unchanged", name), nil
	default:
		return "", fmt.Errorf("health target %q: %w", name, err)
	}
}

func runAddHealthTarget(c *cmdContext) {
	msg, err := addHealthTarget(c.dir.AddHealthTarget, c.args[1], c.args[2])
	if err != nil {
		log.Fatalf("hermex-admin: %v", err)
	}
	fmt.Println(msg)
}
