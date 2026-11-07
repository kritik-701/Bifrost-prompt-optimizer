package optimizer

// EstimateTokens returns a rough token count without needing a real
// tokenizer: about one token per 4 ASCII characters and one token per 2
// non-ASCII characters. Real tokenizers differ per model, so treat results as
// estimates. They are accurate enough to compare "before" and "after".
func EstimateTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + (other+1)/2
}
