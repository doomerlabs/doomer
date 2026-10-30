package adversarylabs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type blobChallenge struct {
	ID    string `json:"id"`
	Nonce string `json:"nonce"`
	SHA   string `json:"sha256"`
	Size  int    `json:"size"`
}

func blobPossessionResponse(nonce, hash string, size int, data []byte) (string, error) {
	key, err := hex.DecodeString(nonce)
	if err != nil || len(key) != 32 {
		return "", fmt.Errorf("invalid blob challenge nonce")
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "doomer-blob-possession-v1\x00%s\x00%d\x00", hash, size)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Proof failure never skips an upload. Only a validated acknowledgement from the
// selected service removes a file from the normal missing-content request.
func (c Client) proveWorkspaceBlobs(ctx context.Context, token string, missing []workspaceMissing, blobs map[string][]byte) (map[string]bool, error) {
	reused := map[string]bool{}
	for offset := 0; offset < len(missing); {
		end, batchBytes := offset, 0
		for end < len(missing) && end-offset < 64 && (end == offset || batchBytes+missing[end].Size <= 64<<20) {
			batchBytes += missing[end].Size
			end++
		}
		files := missing[offset:end]
		input := make([]map[string]any, 0, len(files))
		expected := map[string]int{}
		for _, file := range files {
			input = append(input, map[string]any{"sha256": file.SHA, "size": file.Size})
			expected[file.SHA] = file.Size
		}
		var issued struct {
			Algorithm  string          `json:"algorithm"`
			Challenges []blobChallenge `json:"challenges"`
		}
		if err := c.postJSON(ctx, "/v1/repositories/proofs", map[string]any{"action": "challenge", "files": input}, token, &issued); err != nil {
			return reused, err
		}
		if issued.Algorithm != "hmac-sha256-full-file-v1" || len(issued.Challenges) != len(expected) {
			return reused, fmt.Errorf("invalid blob challenge response")
		}
		answers := make([]map[string]string, 0, len(files))
		seen := map[string]bool{}
		ids := map[string]bool{}
		for _, challenge := range issued.Challenges {
			size, ok := expected[challenge.SHA]
			if !ok || size != challenge.Size || seen[challenge.SHA] || ids[challenge.ID] || len(challenge.ID) != 36 {
				return reused, fmt.Errorf("blob challenge does not match requested content")
			}
			seen[challenge.SHA] = true
			ids[challenge.ID] = true
			data, ok := blobs[challenge.SHA]
			if !ok || len(data) != size {
				return reused, fmt.Errorf("blob challenge content unavailable")
			}
			response, err := blobPossessionResponse(challenge.Nonce, challenge.SHA, size, data)
			if err != nil {
				return reused, err
			}
			answers = append(answers, map[string]string{"id": challenge.ID, "response": response})
		}
		var result struct {
			Reused []string `json:"reused"`
		}
		if err := c.postJSON(ctx, "/v1/repositories/proofs", map[string]any{"action": "prove", "proofs": answers}, token, &result); err != nil {
			return reused, err
		}
		batch := map[string]bool{}
		for _, hash := range result.Reused {
			if _, ok := expected[hash]; !ok || batch[hash] {
				return reused, fmt.Errorf("invalid reused blob acknowledgement")
			}
			batch[hash] = true
		}
		for hash := range batch {
			reused[hash] = true
		}
		offset = end
	}
	return reused, nil
}
