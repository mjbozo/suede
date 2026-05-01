package deflate

import (
	"testing"
)

func TestNegotiateReturnsValidConfigForDefaultRequest(t *testing.T) {
	header := string(DefaultDeflateConfig().Header())
	config := Negotiate(header)
	if config == nil {
		t.Error("expected config for valid offer, got nil")
	}
}

func TestNegotiateReturnsNilConfigForInvalidRequest(t *testing.T) {
	config := Negotiate("some-other-extension")
	if config != nil {
		t.Error("expected nil config when permessage-deflate not in offer")
	}
}

func TestNegotiateReturnsValidConfigIfOnlyClientNoContextTakeoverRequested(t *testing.T) {
	header := "permessage-deflate;client_no_context_takeover;"
	config := Negotiate(header)
	if config == nil {
		t.Error("expected config if only missing server_no_context_takeover")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestNegotiateReturnsValidConfigIfOnlyServerNoContextTakeoverRequested(t *testing.T) {
	header := "permessage-deflate;server_no_context_takeover;"
	config := Negotiate(header)
	if config == nil {
		t.Error("expected config if only missing client_no_context_takeover")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestNegotiateReturnsValidConfigIfOnlyMaxBitsOmitted(t *testing.T) {
	header := "permessage-deflate;server_no_context_takeover;client_no_context_takeover;"
	config := Negotiate(header)
	if config == nil {
		t.Error("expected config if only missing client_no_context_takeover")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestNegotiateReturnsNilWhenClientMaxBitsLessThan15(t *testing.T) {
	header := "permessage-deflate;client_max_window_bits=10;"
	config := Negotiate(header)
	if config != nil {
		t.Error("expected config to be nil when header missing server_no_context_takeover")
	}
}

func TestNegotiateReturnsNilWhenServerMaxBitsLessThan15(t *testing.T) {
	header := "permessage-deflate;server_max_window_bits=10;"
	config := Negotiate(header)
	if config != nil {
		t.Error("expected config to be nil when header missing server_no_context_takeover")
	}
}

func TestNegotiateReturnsNilWhenMaxBitsLessThan15(t *testing.T) {
	header := "permessage-deflate;client_max_window_bits=14;server_max_window_bits=14;"
	config := Negotiate(header)
	if config != nil {
		t.Error("expected config to be nil when header missing server_no_context_takeover")
	}
}

func TestNegotiatePickValidOptionWhenMultipleOffered(t *testing.T) {
	header := "permessage-deflate;client_max_window_bits=14;server_max_window_bits=14,"
	header += "permessage-deflate;client_no_context_takeover;server_no_context_takeover"
	config := Negotiate(header)
	if config == nil {
		t.Error("expected config when valid option offered")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestParseReturnsConfigWithValidExtensionHeader(t *testing.T) {
	header := "permessage-deflate; client_no_context_takeover; server_no_context_takeover;"
	header += "client_max_window_bits=15; server_max_window_bits=15"
	config, err := Parse(header)
	if err != nil {
		t.Errorf("expected valid config, got error: %v\n", err)
	}

	if config == nil {
		t.Error("expected config when valid option offered")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestParseReturnsErrorWithInvalidExtensionHeader(t *testing.T) {
	header := "permessage-deflate; "
	header += "client_max_window_bits=abc; server_max_window_bits=15"
	config, err := Parse(header)
	if err == nil {
		t.Error("expected error when invalid extension header received")
	}

	if config != nil {
		t.Error("expected nil config when invalid option offered")
	}
}

func TestParseReturnsConfigWhenMaxBitsOmitted(t *testing.T) {
	header := "permessage-deflate; client_no_context_takeover; server_no_context_takeover"
	config, err := Parse(header)
	if err != nil {
		t.Errorf("expected valid config, got error: %v\n", err)
	}

	if config == nil {
		t.Error("expected config when valid option offered")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}

func TestParseReturnsErrorWhenClientMaxWindowBitsLessThan15(t *testing.T) {
	header := "permessage-deflate; client_no_context_takeover; server_no_context_takeover; "
	header += "client_max_window_bits=14; server_max_window_bits=15"
	config, err := Parse(header)
	if err != nil {
		t.Error("expected no error when window bits < 15 extension header received")
	}

	if config == nil {
		t.Error("expected non-nil config when window bits < 15 option offered")
	}

	if config.clientNoContextTakeover {
		t.Error("expected client to be disabled when window bits < 15")
	}
}

func TestParseReturnsConfigWithDisabledWhenServerMaxWindowBitsLessThan15(t *testing.T) {
	header := "permessage-deflate; client_no_context_takeover; server_no_context_takeover; "
	header += "client_max_window_bits=15; server_max_window_bits=14"
	config, err := Parse(header)
	if err != nil {
		t.Error("expected no error when window bits < 15 extension header received")
	}

	if config == nil {
		t.Error("expected non-nil config when window bits < 15 option offered")
	}

	if config.serverNoContextTakeover {
		t.Error("expected server to be disabled when window bits < 15")
	}
}

func TestParseReturnsConfigWhenValidOptionExists(t *testing.T) {
	header := "permessage-deflate; client_max_window_bits=15; server_max_window_bits=abc,"
	header += "permessage-deflate; client_no_context_takeover; server_no_context_takeover"
	config, err := Parse(header)
	if err != nil {
		t.Errorf("expected valid config, got error: %v\n", err)
	}

	if config == nil {
		t.Error("expected config when valid option offered")
	}

	if !config.clientNoContextTakeover {
		t.Error("expected negotiation response to include client_no_context_takeover")
	}

	if !config.serverNoContextTakeover {
		t.Error("expected negotiation response to include server_no_context_takeover")
	}
}
