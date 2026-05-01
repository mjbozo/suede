package suede

const (
	FINAL_FRAGMENT             byte = 0x80
	OP_CONTINUE_FRAME          byte = 0x00
	OP_TEXT_FRAME              byte = 0x01
	OP_BINARY_FRAME            byte = 0x02
	OP_NCTRL_RSVD1             byte = 0x03
	OP_NCTRL_RSVD2             byte = 0x04
	OP_NCTRL_RSVD3             byte = 0x05
	OP_NCTRL_RSVD4             byte = 0x06
	OP_NCTRL_RSVD5             byte = 0x07
	OP_CLOSE_CONN              byte = 0x08
	OP_PING                    byte = 0x09
	OP_PONG                    byte = 0x0A
	OP_CTRL_RSVD1              byte = 0x0B
	OP_CTRL_RSVD2              byte = 0x0C
	OP_CTRL_RSVD3              byte = 0x0D
	OP_CTRL_RSVD4              byte = 0x0E
	OP_CTRL_RSVD5              byte = 0x0F
	CLOSE_STATUS_NORMAL        uint = 0x3E8
	CLOSE_STATUS_GOING         uint = 0x3E9
	CLOSE_STATUS_PROTOCOL_ERR  uint = 0x3EA
	CLOSE_STATUS_DATATYPE_ERR  uint = 0x3EB
	CLOSE_STATUS_TYPE_MISMATCH uint = 0x3EF
	CLOSE_STATUS_VIOLATION     uint = 0x3F0
	CLOSE_STATUS_TOO_BIG       uint = 0x3F1
	CLOSE_STATUS_NEGOTIATE_ERR uint = 0x3F2
	CLOSE_STATUS_UNEXPECTED    uint = 0x3F3
	RSV1_MASK                  byte = 0b01000000
	RSV2_MASK                  byte = 0b00100000
	RSV3_MASK                  byte = 0b00010000
)

var allowedCloseCodes = map[uint16]bool{
	uint16(CLOSE_STATUS_NORMAL):        true,
	uint16(CLOSE_STATUS_GOING):         true,
	uint16(CLOSE_STATUS_PROTOCOL_ERR):  true,
	uint16(CLOSE_STATUS_DATATYPE_ERR):  true,
	uint16(CLOSE_STATUS_TYPE_MISMATCH): true,
	uint16(CLOSE_STATUS_VIOLATION):     true,
	uint16(CLOSE_STATUS_TOO_BIG):       true,
	uint16(CLOSE_STATUS_NEGOTIATE_ERR): true,
	uint16(CLOSE_STATUS_UNEXPECTED):    true,
}
