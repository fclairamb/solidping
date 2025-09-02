package passwords

import (
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	t.Parallel()

	password := "testpassword123"

	hash, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash == "" {
		t.Fatal("Hash returned empty string")
	}

	// Verify correct password
	if !Verify(password, hash) {
		t.Error("Verify failed for correct password")
	}

	// Verify wrong password
	if Verify("wrongpassword", hash) {
		t.Error("Verify succeeded for wrong password")
	}
}

func TestVerifyInvalidHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"invalid format", "not-a-valid-hash"},
		{"wrong algorithm", "$bcrypt$v=19$m=65536,t=3,p=4$salt$hash"},
		{"too few parts", "$argon2id$v=19$m=65536,t=3,p=4$salt"},
		{"invalid base64 salt", "$argon2id$v=19$m=65536,t=3,p=4$!!!invalid!!!$aGFzaA"},
		{"invalid base64 hash", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$!!!invalid!!!"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if Verify("password", testCase.hash) {
				t.Errorf("Verify should return false for invalid hash: %s", testCase.hash)
			}
		})
	}
}

func TestHashUniqueness(t *testing.T) {
	t.Parallel()

	password := "samepassword"

	hash1, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	hash2, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Two hashes of the same password should be different (different salts)")
	}

	// Both should still verify correctly
	if !Verify(password, hash1) {
		t.Error("Verify failed for hash1")
	}

	if !Verify(password, hash2) {
		t.Error("Verify failed for hash2")
	}
}
