package crypt

// hash64Alphabet is the 64-character alphabet crypt(3) encodes a digest with. It
// is not RFC 4648 base64: the order is "./" then digits, then upper, then lower.
const hash64Alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// hash64 encodes bytes the way crypt(3) does: little-endian 24-bit groups, six
// bits per character, no padding. A trailing group of one or two bytes emits two
// or three characters. The digest bytes are fed in the scheme's own permuted
// order (see sha512Crypt and md5Crypt), which is why this takes the bytes as
// given rather than permuting anything itself.
func hash64(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, 0, (len(src)*8+5)/6)
	i := 0
	for ; i+3 <= len(src); i += 3 {
		v := uint(src[i]) | uint(src[i+1])<<8 | uint(src[i+2])<<16
		dst = append(dst,
			hash64Alphabet[v&0x3f],
			hash64Alphabet[v>>6&0x3f],
			hash64Alphabet[v>>12&0x3f],
			hash64Alphabet[v>>18&0x3f],
		)
	}
	switch len(src) - i {
	case 1:
		v := uint(src[i])
		dst = append(dst, hash64Alphabet[v&0x3f], hash64Alphabet[v>>6&0x3f])
	case 2:
		v := uint(src[i]) | uint(src[i+1])<<8
		dst = append(dst, hash64Alphabet[v&0x3f], hash64Alphabet[v>>6&0x3f], hash64Alphabet[v>>12&0x3f])
	}
	return dst
}

// permute reorders digest bytes into the sequence a scheme's output encoding
// expects, so the caller states the order once as an index table.
func permute(digest []byte, order []int) []byte {
	out := make([]byte, len(order))
	for i, idx := range order {
		out[i] = digest[idx]
	}
	return out
}

// repeatTo returns length bytes made by repeating src, truncating the last copy.
// Both schemes build their password and salt sequences this way.
func repeatTo(src []byte, length int) []byte {
	out := make([]byte, length)
	for i := 0; i < length; i += len(src) {
		copy(out[i:], src)
	}
	return out
}
