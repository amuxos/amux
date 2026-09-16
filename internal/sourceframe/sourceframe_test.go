package sourceframe

import "testing"

func TestFormatInputUsesStableDisplayLabels(t *testing.T) {
	tests := []struct {
		name   string
		source string
		text   string
		want   string
	}{
		{name: "cli", source: SourceCLI, text: "hello", want: "\r\n💻 来自终端: hello\r\n"},
		{name: "desktop", source: SourceDesktop, text: "hello", want: "\r\n🖥️ 来自桌面: hello\r\n"},
		{name: "lark", source: SourceLark, text: "hello", want: "\r\n📱 来自飞书: hello\r\n"},
		{name: "feishu alias", source: "feishu", text: "hello", want: "\r\n📱 来自飞书: hello\r\n"},
		{name: "empty source defaults to cli", source: "", text: "hello", want: "\r\n💻 来自终端: hello\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(FormatInput(tt.source, tt.text)); got != tt.want {
				t.Fatalf("FormatInput(%q, %q) = %q, want %q", tt.source, tt.text, got, tt.want)
			}
		})
	}
}

func TestClassifyRecognizesLegacyDisplayFrames(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		wantSource string
		wantInput  bool
	}{
		{name: "cli", data: []byte("\r\n💻 来自终端: hello\r\n"), wantSource: SourceCLI, wantInput: true},
		{name: "desktop", data: []byte("\r\n🖥️ 来自桌面: hello\r\n"), wantSource: SourceDesktop, wantInput: true},
		{name: "lark", data: []byte("\r\n📱 来自飞书: hello\r\n"), wantSource: SourceLark, wantInput: true},
		{name: "agent", data: []byte("agent output"), wantSource: SourceAgent, wantInput: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.data)
			if got.Source != tt.wantSource || got.Input != tt.wantInput {
				t.Fatalf("Classify(%q) = %+v, want source=%q input=%v", tt.data, got, tt.wantSource, tt.wantInput)
			}
		})
	}
}

func TestExtractInputReturnsOriginalText(t *testing.T) {
	for _, source := range []string{SourceCLI, SourceDesktop, SourceLark, "feishu"} {
		frame := FormatInput(source, "  continue analysis  ")
		gotSource, gotText, ok := ExtractInput(frame)
		if !ok || gotText != "continue analysis" {
			t.Fatalf("ExtractInput(%q) = %q %q %v", source, gotSource, gotText, ok)
		}
		wantSource := source
		if source == "feishu" {
			wantSource = SourceLark
		}
		if gotSource != wantSource {
			t.Fatalf("ExtractInput(%q) source = %q, want %q", source, gotSource, wantSource)
		}
	}
	if _, _, ok := ExtractInput([]byte("assistant output")); ok {
		t.Fatal("assistant output classified as input")
	}
}
