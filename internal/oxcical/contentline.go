package oxcical

// ContentLines splits an iCalendar stream into its logical content lines, joining
// the continuation lines RFC 5545 section 3.1 folds a long line into.
func ContentLines(raw []byte) []string {
	return unfold(raw)
}

// SplitContentLine splits one logical content line into its property name, its
// parameters (upper-case names, values unquoted) and its raw value. The value
// starts after the first colon outside a quoted parameter value, because a
// parameter such as SENT-BY="mailto:a@example.org" holds a colon of its own.
func SplitContentLine(line string) (name string, params map[string][]string, value string) {
	return splitLine(line)
}
