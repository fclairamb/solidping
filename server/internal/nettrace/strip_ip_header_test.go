package nettrace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripIPHeader(t *testing.T) {
	t.Parallel()

	payload := []byte{9, 8, 7}
	v4 := append([]byte{0x45}, make([]byte, 19)...)
	v4Options := append([]byte{0x46}, make([]byte, 23)...)

	tests := []struct {
		name   string
		quoted []byte
		isV6   bool
		want   []byte
	}{
		{"v4 plain", append(append([]byte{}, v4...), payload...), false, payload},
		{"v4 with options", append(append([]byte{}, v4Options...), payload...), false, payload},
		{"v4 too short", make([]byte, 10), false, nil},
		{"v4 header length below minimum", append([]byte{0x41}, make([]byte, 30)...), false, nil},
		{"v4 truncated options", append([]byte{0x4f}, make([]byte, 30)...), false, nil},
		{"v6 plain", append(make([]byte, 40), payload...), true, payload},
		{"v6 too short", make([]byte, 39), true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := stripIPHeader(tt.quoted, tt.isV6)
			if tt.want == nil {
				require.Nil(t, got)

				return
			}
			require.Equal(t, tt.want, got)
		})
	}
}
