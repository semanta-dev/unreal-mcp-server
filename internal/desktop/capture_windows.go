//go:build windows

package desktop

import (
	"fmt"
	"image"
	"unsafe"
)

// GDI raster-op and PrintWindow flags.
const (
	srcCopy             = 0x00CC0020
	captureBlt          = 0x40000000 // include layered/overlapping windows in the blit
	pwRenderFullContent = 0x00000002 // PrintWindow: render DWM/hardware-accelerated content
	biRGB               = 0
	dibRGBColors        = 0
)

// bitmapInfoHeader is BITMAPINFOHEADER (winGDI.h). A trailing color slot gives
// GDI room to scribble even though 32bpp BI_RGB needs no palette.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [4]uint32
}

// captureRegionImg grabs a rectangle of the virtual desktop from the screen DC.
func captureRegionImg(x, y, w, h int) (*image.RGBA, error) {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(screen) failed")
	}
	defer procReleaseDC.Call(0, screenDC)
	return blitToRGBA(screenDC, screenDC, x, y, w, h)
}

// captureWindowImg captures a window either by rendering it (print, works
// backgrounded) or by blitting its on-screen rectangle (screen).
func captureWindowImg(hwnd uintptr, method string) (*image.RGBA, error) {
	rect, ok := windowRect(hwnd)
	if !ok || rect.empty() {
		return nil, fmt.Errorf("could not read window rectangle")
	}
	w, h := rect.W, rect.H

	if method == "screen" {
		screenDC, _, _ := procGetDC.Call(0)
		if screenDC == 0 {
			return nil, fmt.Errorf("GetDC(screen) failed")
		}
		defer procReleaseDC.Call(0, screenDC)
		return blitToRGBA(screenDC, screenDC, rect.X, rect.Y, w, h)
	}

	// print: PrintWindow into an offscreen bitmap.
	winDC, _, _ := procGetDC.Call(hwnd)
	if winDC == 0 {
		return nil, fmt.Errorf("GetDC(window) failed")
	}
	defer procReleaseDC.Call(hwnd, winDC)

	memDC, _, _ := procCreateCompatibleDC.Call(winDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)
	hbm, _, _ := procCreateCompatibleBitmap.Call(winDC, uintptr(w), uintptr(h))
	if hbm == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbm)
	old, _, _ := procSelectObject.Call(memDC, hbm)
	r, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
	procSelectObject.Call(memDC, old) // deselect before GetDIBits
	if r == 0 {
		return nil, fmt.Errorf("PrintWindow failed (try method=screen with focus=true)")
	}
	return dibToRGBA(memDC, hbm, w, h)
}

// blitToRGBA BitBlts a source rectangle into a fresh top-down DIB and returns it
// as an RGBA image. compatDC provides the pixel format for the offscreen bitmap;
// srcDC is the blit source.
func blitToRGBA(compatDC, srcDC uintptr, srcX, srcY, w, h int) (*image.RGBA, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid capture size %dx%d", w, h)
	}
	memDC, _, _ := procCreateCompatibleDC.Call(compatDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)
	hbm, _, _ := procCreateCompatibleBitmap.Call(compatDC, uintptr(w), uintptr(h))
	if hbm == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbm)
	old, _, _ := procSelectObject.Call(memDC, hbm)
	r, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), srcDC, cint(srcX), cint(srcY), srcCopy|captureBlt)
	procSelectObject.Call(memDC, old) // deselect before GetDIBits
	if r == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	return dibToRGBA(memDC, hbm, w, h)
}

// dibToRGBA pulls a bitmap's pixels via GetDIBits as top-down 32bpp BGRA and
// converts to a Go RGBA image (opaque alpha — GDI leaves the 4th byte unset).
// The bitmap MUST NOT be selected into any DC when this is called.
func dibToRGBA(hdc, hbm uintptr, w, h int) (*image.RGBA, error) {
	buf := make([]byte, w*h*4)
	bmi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // negative => top-down rows
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	r, _, _ := procGetDIBits.Call(hdc, hbm, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bmi)), dibRGBColors)
	if r == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// BGRA (Windows DWORD 0x00RRGGBB, little-endian) -> RGBA, force opaque.
	for i := 0; i+3 < len(buf) && i+3 < len(img.Pix); i += 4 {
		img.Pix[i] = buf[i+2]   // R
		img.Pix[i+1] = buf[i+1] // G
		img.Pix[i+2] = buf[i]   // B
		img.Pix[i+3] = 255      // A
	}
	return img, nil
}
