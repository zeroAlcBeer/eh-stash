package task

import "testing"

func TestNormalizeBaseTitle(t *testing.T) {
	tests := []struct {
		titleJPN string
		title    string
		want     string
	}{
		{"", "", ""},
		{"テスト タイトル", "", "テストタイトル"},
		{"テスト [中国翻訳]", "", "テスト"},
		{"テスト タイトル [中国翻訳]", "", "テストタイトル"},
		{"タイトル  スペース  多い", "", "タイトルスペース多い"},
		{"[作者] タイトル (シリーズ) [中国翻訳]", "", "[作者]タイトル(シリーズ)"},
		{"普通のタイトル", "", "普通のタイトル"},
		// Fallback to title when title_jpn is empty
		{"", "English Title Here", "EnglishTitleHere"},
		{"", "[Artist] Cosplay Set", "[Artist]CosplaySet"},
		// title_jpn takes priority
		{"日本語タイトル", "English Title", "日本語タイトル"},
	}
	for _, tt := range tests {
		got := NormalizeBaseTitle(tt.titleJPN, tt.title)
		if got != tt.want {
			t.Errorf("NormalizeBaseTitle(%q, %q) = %q, want %q", tt.titleJPN, tt.title, got, tt.want)
		}
	}
}
