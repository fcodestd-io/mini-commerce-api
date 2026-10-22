package main

import "testing"

func TestValidateProduct(t *testing.T) {
	tests := []struct {
		name    string
		input   ProductInput
		wantErr bool
	}{
		{
			name: "valid product",
			input: ProductInput{
				Name:  "Mouse Wireless",
				Price: 75000,
				Stock: 10,
			},
			wantErr: false,
		},
		{
			name: "empty name",
			input: ProductInput{
				Name:  "   ",
				Price: 75000,
				Stock: 10,
			},
			wantErr: true,
		},
		{
			name: "negative price",
			input: ProductInput{
				Name:  "Mouse Wireless",
				Price: -1,
				Stock: 10,
			},
			wantErr: true,
		},
		{
			name: "negative stock",
			input: ProductInput{
				Name:  "Mouse Wireless",
				Price: 75000,
				Stock: -1,
			},
			wantErr: true,
		},
		{
			name: "invalid category ID",
			input: ProductInput{
				CategoryID: int64Pointer(0),
				Name:       "Mouse Wireless",
				Price:      75000,
				Stock:      10,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateProduct(tt.input)

			if tt.wantErr && got == "" {
				t.Fatal("expected validation error, got none")
			}

			if !tt.wantErr && got != "" {
				t.Fatalf("expected no error, got %q", got)
			}
		})
	}
}

func TestParsePositiveInt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		fallback int
		want     int
	}{
		{
			name:     "valid number",
			input:    "5",
			fallback: 1,
			want:     5,
		},
		{
			name:     "empty input uses fallback",
			input:    "",
			fallback: 10,
			want:     10,
		},
		{
			name:     "zero uses fallback",
			input:    "0",
			fallback: 10,
			want:     10,
		},
		{
			name:     "invalid input uses fallback",
			input:    "abc",
			fallback: 10,
			want:     10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePositiveInt(tt.input, tt.fallback)

			if got != tt.want {
				t.Fatalf("parsePositiveInt(%q) = %d, want %d",
					tt.input, got, tt.want)
			}
		})
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
