package embedding

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0.0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1.0},
		{"similar", []float32{1, 1}, []float32{1, 0.9}, 0.99},
		{"empty", nil, nil, 0},
		{"length mismatch", []float32{1}, []float32{1, 2}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 0.02 {
				t.Errorf("CosineSimilarity = %f, want ~%f", got, tt.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	v := Normalize([]float32{3, 4})
	// 3/5 = 0.6, 4/5 = 0.8
	if math.Abs(float64(v[0]-0.6)) > 0.001 || math.Abs(float64(v[1]-0.8)) > 0.001 {
		t.Errorf("Normalize([3,4]) = %v, want [0.6, 0.8]", v)
	}
	// Unit length
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if math.Abs(float64(sum-1.0)) > 0.001 {
		t.Errorf("magnitude = %f, want 1.0", sum)
	}
}

func TestNormalizeZero(t *testing.T) {
	v := Normalize([]float32{0, 0, 0})
	for i, x := range v {
		if x != 0 {
			t.Errorf("v[%d] = %f, want 0", i, x)
		}
	}
}

func TestEncodeDecodeFloat32(t *testing.T) {
	orig := []float32{1.5, -2.3, 0.0, 3.14159}
	encoded := EncodeFloat32(orig)
	decoded := DecodeFloat32(encoded)

	if len(decoded) != len(orig) {
		t.Fatalf("len = %d, want %d", len(decoded), len(orig))
	}
	for i := range orig {
		if decoded[i] != orig[i] {
			t.Errorf("[%d] = %f, want %f", i, decoded[i], orig[i])
		}
	}
}

func TestEncodeDecodeEmpty(t *testing.T) {
	encoded := EncodeFloat32(nil)
	decoded := DecodeFloat32(encoded)
	if len(decoded) != 0 {
		t.Errorf("decoded empty = %v, want []", decoded)
	}
}
