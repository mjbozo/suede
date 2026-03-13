package suede

const (
	FINAL_FRAGMENT      byte = 0x80
	OP_CONTINUE_FRAME   byte = 0x00
	OP_TEXT_FRAME       byte = 0x01
	OP_BINARY_FRAME     byte = 0x02
	OP_CLOSE_CONN       byte = 0x08
	OP_PING             byte = 0x09
	OP_PONG             byte = 0x0A
	CLOSE_STATUS_NORMAL uint = 0x3E8
)
