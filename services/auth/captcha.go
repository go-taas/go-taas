// Image CAPTCHA for the local login form.
//
// The bootstrap admin account is exempt from email verification, so a
// password-only login would otherwise be open to brute force. When
// auth.captcha.enabled is on, GetCaptcha issues a short-lived, single-use
// puzzle and Login refuses to check the password until it is solved.
//
// The answer is stored in Redis under auth:captcha:{id} with a short TTL
// (the same component the email codes use). The image is rendered as a
// PNG data URL with a built-in 5x7 bitmap font, so no font file or extra
// dependency is needed.

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

const (
	// captchaTTL is how long a puzzle stays valid.
	captchaTTL = 3 * time.Minute
	// captchaKeyPrefix is the Redis key prefix for the stored answers.
	captchaKeyPrefix = "auth:captcha:"
	// captchaLen is the number of characters in a puzzle.
	captchaLen = 4
	// captchaAlphabet skips visually ambiguous characters (I, O, 0, 1).
	captchaAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	captchaWidth  = 168
	captchaHeight = 56
	// captchaScale is the bitmap-font pixel size multiplier.
	captchaScale = 5
)

// captchaEnabled reports whether the login CAPTCHA is configured.
func captchaEnabled() bool {
	cfg := config.GetConfig()
	return cfg != nil && cfg.Auth.Captcha.Enabled
}

// bootstrapAdminUsername is the seeded admin account's username. It is
// treated as a platform administrator even before the account exists, so
// the very first sign-in into a fresh install is challenged too.
func bootstrapAdminUsername() string {
	cfg := config.GetConfig()
	if cfg != nil && strings.TrimSpace(cfg.Auth.SeedSso.AdminUsername) != "" {
		return strings.TrimSpace(cfg.Auth.SeedSso.AdminUsername)
	}
	return "admin"
}

// captchaRequiredForUsername reports whether a sign-in as username must
// solve the image CAPTCHA. Only platform administrators are challenged:
// they are exempt from email verification, so the CAPTCHA is the only
// barrier in front of their password. Ordinary accounts keep the email
// verification code and are not asked for a CAPTCHA.
func (s *Service) captchaRequiredForUsername(ctx context.Context, username string) bool {
	if !captchaEnabled() {
		return false
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	// The seeded admin account counts even before it is provisioned.
	if strings.EqualFold(username, bootstrapAdminUsername()) {
		return true
	}
	repo, err := s.ssoRepository()
	if err != nil {
		return false
	}
	user, err := repo.FindUserByUsername(ctx, username)
	if err != nil || user == nil {
		return false
	}
	if user.PlatformAdmin {
		return true
	}
	return s.isPlatformAdminUser(ctx, user.ID)
}

// GetCaptcha issues a new puzzle for a platform-administrator sign-in.
// For an ordinary account - or when the CAPTCHA is disabled - the
// response carries neither an id nor an image, which tells the console to
// hide the field.
func (s *Service) GetCaptcha(ctx context.Context, req *authv1.GetCaptchaRequest) (*authv1.GetCaptchaResponse, error) {
	if !s.captchaRequiredForUsername(ctx, req.GetUsername()) {
		return &authv1.GetCaptchaResponse{Response: okResponse()}, nil
	}
	code, err := randomCaptchaCode()
	if err != nil {
		return nil, err
	}
	imageData, err := renderCaptchaPNG(code)
	if err != nil {
		return nil, err
	}
	client, err := s.redisClientFor()
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if err := client.Set(ctx, captchaKeyPrefix+id, code, captchaTTL).Err(); err != nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: store captcha failed: %v", err)
	}
	return &authv1.GetCaptchaResponse{
		Response:  okResponse(),
		CaptchaId: id,
		Image:     imageData,
		ExpiresAt: time.Now().Add(captchaTTL).Unix(),
	}, nil
}

// verifyCaptcha checks a submitted answer and consumes it, so one puzzle
// authorises at most one login attempt.
func (s *Service) verifyCaptcha(ctx context.Context, id, answer string) error {
	id = strings.TrimSpace(id)
	answer = strings.ToUpper(strings.TrimSpace(answer))
	if id == "" || answer == "" {
		return apierrors.New(apierrors.CodeCaptchaInvalid)
	}
	client, err := s.redisClientFor()
	if err != nil {
		return err
	}
	key := captchaKeyPrefix + id
	stored, err := client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return apierrors.New(apierrors.CodeCaptchaInvalid)
		}
		return apierrors.Newf(apierrors.CodeInternal, "auth: read captcha failed: %v", err)
	}
	// Delete before comparing: a wrong answer burns the puzzle too, so a
	// caller cannot keep guessing against one image.
	_ = client.Del(ctx, key).Err()
	if subtle.ConstantTimeCompare([]byte(strings.ToUpper(stored)), []byte(answer)) != 1 {
		return apierrors.New(apierrors.CodeCaptchaInvalid)
	}
	return nil
}

// randomCaptchaCode draws captchaLen characters from captchaAlphabet
// using the cryptographic RNG.
func randomCaptchaCode() (string, error) {
	max := big.NewInt(int64(len(captchaAlphabet)))
	var b strings.Builder
	b.Grow(captchaLen)
	for i := 0; i < captchaLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", apierrors.Newf(apierrors.CodeInternal, "auth: generate captcha failed: %v", err)
		}
		b.WriteByte(captchaAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// renderCaptchaPNG draws code on a noisy background and returns it as a
// PNG data URL.
func renderCaptchaPNG(code string) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, captchaWidth, captchaHeight))
	bg := color.RGBA{R: 245, G: 246, B: 248, A: 255}
	for y := 0; y < captchaHeight; y++ {
		for x := 0; x < captchaWidth; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	// Interference lines first, so the glyphs stay readable.
	for i := 0; i < 3; i++ {
		x0, y0, x1, y1 := randInt(captchaWidth), randInt(captchaHeight),
			randInt(captchaWidth), randInt(captchaHeight)
		drawLine(img, x0, y0, x1, y1, randNoiseColor())
	}
	// Glyphs: each character is slightly sheared and offset vertically.
	step := (captchaWidth - 16) / captchaLen
	for i := 0; i < len(code); i++ {
		glyph, ok := captchaGlyphs[code[i]]
		if !ok {
			continue
		}
		x := 8 + i*step + randInt(5) - 2
		y := 10 + randInt(9) - 4
		shear := randInt(3) - 1
		drawGlyph(img, glyph, x, y, shear, randInkColor())
	}
	// Noise dots.
	for i := 0; i < 140; i++ {
		img.SetRGBA(randInt(captchaWidth), randInt(captchaHeight), randNoiseColor())
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", apierrors.Newf(apierrors.CodeInternal, "auth: encode captcha failed: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// drawGlyph expands one 5x7 bitmap font row by row, shearing the rows so
// the character does not map cleanly onto a fixed grid.
func drawGlyph(img *image.RGBA, glyph [7]uint8, x, y, shear int, c color.RGBA) {
	for row, bits := range glyph {
		for col := 0; col < 5; col++ {
			if bits&(1<<uint(4-col)) == 0 {
				continue
			}
			px := x + col*captchaScale + row*shear
			py := y + row*captchaScale
			for dy := 0; dy < captchaScale; dy++ {
				for dx := 0; dx < captchaScale; dx++ {
					img.SetRGBA(px+dx, py+dy, c)
				}
			}
		}
	}
}

// drawLine plots a one-pixel Bresenham line.
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		img.SetRGBA(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// randInt returns a uniform int in [0, n).
func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// randInkColor picks a dark colour for the glyphs.
func randInkColor() color.RGBA {
	return captchaInkColors[randInt(len(captchaInkColors))]
}

// randNoiseColor picks a mid-tone colour for the interference lines and
// dots.
func randNoiseColor() color.RGBA {
	return captchaNoiseColors[randInt(len(captchaNoiseColors))]
}

// captchaInkColors and captchaNoiseColors are fixed palettes; drawing
// from them avoids floaty per-channel arithmetic while still varying the
// image on every render.
var (
	captchaInkColors = []color.RGBA{
		{R: 24, G: 39, B: 84, A: 255},
		{R: 74, G: 32, B: 92, A: 255},
		{R: 20, G: 74, B: 66, A: 255},
		{R: 98, G: 34, B: 30, A: 255},
		{R: 32, G: 52, B: 24, A: 255},
	}
	captchaNoiseColors = []color.RGBA{
		{R: 150, G: 168, B: 196, A: 255},
		{R: 196, G: 160, B: 172, A: 255},
		{R: 168, G: 196, B: 172, A: 255},
		{R: 190, G: 182, B: 150, A: 255},
		{R: 132, G: 148, B: 172, A: 255},
	}
)

// captchaGlyphs is a 5x7 bitmap font covering captchaAlphabet.
var captchaGlyphs = map[byte][7]uint8{
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C': {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'D': {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'E': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'G': {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01110},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'J': {0b00111, 0b00010, 0b00010, 0b00010, 0b00010, 0b10010, 0b01100},
	'K': {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'L': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'M': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N': {0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001, 0b10001},
	'P': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'Q': {0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101},
	'R': {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	'S': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'U': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'V': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'W': {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001},
	'X': {0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001},
	'Y': {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'Z': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11111, 0b00010, 0b00100, 0b00010, 0b00001, 0b10001, 0b01110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6': {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
}
