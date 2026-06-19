package lexer

// Lexer turns a SQL string into a stream of tokens, one NextToken() call at a
// time. It is hand-written (no generator) so we control error positions and
// keyword handling precisely — see docs/decisions for the rationale.
//
// The lexer operates on bytes. SQL keywords, identifiers, and operators are
// all ASCII; the only place non-ASCII can appear is inside a string literal,
// where bytes are copied through verbatim, so byte-level scanning is correct
// and keeps column tracking simple.
type Lexer struct {
	input   string
	pos     int  // offset of ch (the char currently under examination)
	readPos int  // offset of the next char to read
	ch      byte // current char; 0 means end-of-input
	line    int  // 1-based line of ch
	col     int  // 1-based column of ch
}

// New creates a Lexer positioned on the first character of input.
func New(input string) *Lexer {
	l := &Lexer{input: input, line: 1, col: 0}
	l.readChar()
	return l
}

// readChar advances one byte, maintaining line/column. A newline rolls the
// line counter and resets the column for the next character.
func (l *Lexer) readChar() {
	if l.ch == '\n' {
		l.line++
		l.col = 0
	}
	if l.readPos >= len(l.input) {
		l.ch = 0
	} else {
		l.ch = l.input[l.readPos]
	}
	l.pos = l.readPos
	l.readPos++
	l.col++
}

// peekChar returns the next byte without consuming it (0 at end-of-input).
func (l *Lexer) peekChar() byte {
	if l.readPos >= len(l.input) {
		return 0
	}
	return l.input[l.readPos]
}

// pos0 captures the position of the char currently under examination.
func (l *Lexer) pos0() Position {
	return Position{Line: l.line, Column: l.col, Offset: l.pos}
}

// NextToken scans and returns the next token. At end of input it returns an
// EOF token repeatedly, so callers can loop until they see EOF.
func (l *Lexer) NextToken() Token {
	l.skipTrivia()

	start := l.pos0()

	switch l.ch {
	case 0:
		return Token{Type: EOF, Pos: start}
	case '=':
		l.readChar()
		return Token{Type: EQ, Literal: "=", Pos: start}
	case '!':
		if l.peekChar() == '=' {
			l.readChar()
			l.readChar()
			return Token{Type: NEQ, Literal: "!=", Pos: start}
		}
		return l.illegal(start)
	case '<':
		switch l.peekChar() {
		case '=':
			l.readChar()
			l.readChar()
			return Token{Type: LTE, Literal: "<=", Pos: start}
		case '>':
			l.readChar()
			l.readChar()
			return Token{Type: NEQ, Literal: "<>", Pos: start}
		default:
			l.readChar()
			return Token{Type: LT, Literal: "<", Pos: start}
		}
	case '>':
		if l.peekChar() == '=' {
			l.readChar()
			l.readChar()
			return Token{Type: GTE, Literal: ">=", Pos: start}
		}
		l.readChar()
		return Token{Type: GT, Literal: ">", Pos: start}
	case '+':
		l.readChar()
		return Token{Type: PLUS, Literal: "+", Pos: start}
	case '-':
		l.readChar()
		return Token{Type: MINUS, Literal: "-", Pos: start}
	case '*':
		l.readChar()
		return Token{Type: STAR, Literal: "*", Pos: start}
	case '/':
		l.readChar()
		return Token{Type: SLASH, Literal: "/", Pos: start}
	case ',':
		l.readChar()
		return Token{Type: COMMA, Literal: ",", Pos: start}
	case ';':
		l.readChar()
		return Token{Type: SEMICOLON, Literal: ";", Pos: start}
	case '(':
		l.readChar()
		return Token{Type: LPAREN, Literal: "(", Pos: start}
	case ')':
		l.readChar()
		return Token{Type: RPAREN, Literal: ")", Pos: start}
	case '.':
		// A dot followed by a digit begins a float (e.g. .5); otherwise it is
		// the qualifier in table.column.
		if isDigit(l.peekChar()) {
			return l.readNumber(start)
		}
		l.readChar()
		return Token{Type: DOT, Literal: ".", Pos: start}
	case '\'':
		return l.readString(start)
	}

	switch {
	case isLetter(l.ch):
		word := l.readIdentifier()
		return Token{Type: LookupIdent(word), Literal: word, Pos: start}
	case isDigit(l.ch):
		return l.readNumber(start)
	default:
		return l.illegal(start)
	}
}

// illegal consumes the offending byte and returns an ILLEGAL token carrying it.
func (l *Lexer) illegal(start Position) Token {
	ch := string(l.ch)
	l.readChar()
	return Token{Type: ILLEGAL, Literal: ch, Pos: start}
}

// skipTrivia consumes whitespace and comments (-- to end of line, and /* */
// block comments) between tokens.
func (l *Lexer) skipTrivia() {
	for {
		switch {
		case l.ch == ' ' || l.ch == '\t' || l.ch == '\n' || l.ch == '\r':
			l.readChar()
		case l.ch == '-' && l.peekChar() == '-':
			for l.ch != '\n' && l.ch != 0 {
				l.readChar()
			}
		case l.ch == '/' && l.peekChar() == '*':
			l.readChar() // '/'
			l.readChar() // '*'
			for !(l.ch == '*' && l.peekChar() == '/') && l.ch != 0 {
				l.readChar()
			}
			if l.ch != 0 {
				l.readChar() // '*'
				l.readChar() // '/'
			}
		default:
			return
		}
	}
}

// readIdentifier reads a run of letters, digits, and underscores. The first
// character has already been validated as a letter by the caller.
func (l *Lexer) readIdentifier() string {
	start := l.pos
	for isLetter(l.ch) || isDigit(l.ch) {
		l.readChar()
	}
	return l.input[start:l.pos]
}

// readNumber reads an integer or float literal. A single embedded dot followed
// by digits makes it a float; anything else terminates the number.
func (l *Lexer) readNumber(start Position) Token {
	begin := l.pos
	isFloat := false
	for isDigit(l.ch) {
		l.readChar()
	}
	if l.ch == '.' && isDigit(l.peekChar()) {
		isFloat = true
		l.readChar() // consume '.'
		for isDigit(l.ch) {
			l.readChar()
		}
	}
	lit := l.input[begin:l.pos]
	if isFloat {
		return Token{Type: FLOAT, Literal: lit, Pos: start}
	}
	return Token{Type: INT, Literal: lit, Pos: start}
}

// readString reads a single-quoted string literal. Two consecutive quotes (”)
// are an escaped single quote. An unterminated string returns ILLEGAL pointing
// at the opening quote.
func (l *Lexer) readString(start Position) Token {
	l.readChar() // consume opening quote
	var sb []byte
	for {
		switch l.ch {
		case 0:
			return Token{Type: ILLEGAL, Literal: "unterminated string", Pos: start}
		case '\'':
			if l.peekChar() == '\'' {
				sb = append(sb, '\'')
				l.readChar() // first quote
				l.readChar() // second quote
				continue
			}
			l.readChar() // consume closing quote
			return Token{Type: STRING, Literal: string(sb), Pos: start}
		default:
			sb = append(sb, l.ch)
			l.readChar()
		}
	}
}

func isLetter(ch byte) bool {
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
