package suede

const (
	FINAL_FRAGMENT      byte = 0x80
	OP_CONTINUE_FRAME   byte = 0x00
	OP_TEXT_FRAME       byte = 0x01
	OP_BINARY_FRAME     byte = 0x02
	OP_NCTRL_RSVD1      byte = 0x03
	OP_NCTRL_RSVD2      byte = 0x04
	OP_NCTRL_RSVD3      byte = 0x05
	OP_NCTRL_RSVD4      byte = 0x06
	OP_NCTRL_RSVD5      byte = 0x07
	OP_CLOSE_CONN       byte = 0x08
	OP_PING             byte = 0x09
	OP_PONG             byte = 0x0A
	OP_CTRL_RSVD1       byte = 0x0B
	OP_CTRL_RSVD2       byte = 0x0C
	OP_CTRL_RSVD3       byte = 0x0D
	OP_CTRL_RSVD4       byte = 0x0E
	OP_CTRL_RSVD5       byte = 0x0F
	CLOSE_STATUS_NORMAL uint = 0x3E8
	CLOSE_STATUS_GOING  uint = 0x3EC
	CLOSE_STATUS_ERROR  uint = 0x3ED
)
