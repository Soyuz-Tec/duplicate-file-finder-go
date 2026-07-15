//go:build windows

package gui

import (
	"fmt"
	"image"
	"math"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

const (
	siigbfResizeToFit = 0x00
)

var (
	shell32                         = syscall.NewLazyDLL("shell32.dll")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	iidIShellItemImageFactory       = win.IID{
		Data1: 0xbcc18b79,
		Data2: 0xba16,
		Data3: 0x442f,
		Data4: [8]byte{0x80, 0xc4, 0x8a, 0x59, 0xc3, 0x0c, 0x46, 0x3b},
	}
)

type shellItemImageFactory struct {
	LpVtbl *shellItemImageFactoryVtbl
}

type shellItemImageFactoryVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetImage       uintptr
}

func (f *shellItemImageFactory) release() {
	if f == nil || f.LpVtbl == nil {
		return
	}
	_, _, _ = syscall.SyscallN(f.LpVtbl.Release, uintptr(unsafe.Pointer(f)))
}

func (f *shellItemImageFactory) getImage(size win.SIZE, flags uint32, hbm *win.HBITMAP) win.HRESULT {
	ret, _, _ := syscall.SyscallN(
		f.LpVtbl.GetImage,
		uintptr(unsafe.Pointer(f)),
		uintptr(unsafe.Pointer(&size)),
		uintptr(flags),
		uintptr(unsafe.Pointer(hbm)),
	)
	return win.HRESULT(ret)
}

// shellThumbnailNRGBAFromHeldRecordPath is the raw path-based Shell boundary.
// Its caller must hold a verified no-write/no-delete-share record handle for
// the entire call and revalidate the record scope afterward.
func shellThumbnailNRGBAFromHeldRecordPath(path string, maxSize int) (*image.NRGBA, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	var factory *shellItemImageFactory
	hrRaw, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(unsafe.Pointer(&iidIShellItemImageFactory)),
		uintptr(unsafe.Pointer(&factory)),
	)
	hr := win.HRESULT(hrRaw)
	if win.FAILED(hr) || factory == nil {
		return nil, fmt.Errorf("the Windows Shell thumbnail provider is unavailable: HRESULT 0x%08x", uint32(hr))
	}
	defer factory.release()

	var hbm win.HBITMAP
	size := win.SIZE{CX: int32(maxSize), CY: int32(maxSize)}
	hr = factory.getImage(size, siigbfResizeToFit, &hbm)
	if win.FAILED(hr) || hbm == 0 {
		return nil, fmt.Errorf("the Windows Shell thumbnail rendering failed: HRESULT 0x%08x", uint32(hr))
	}
	defer win.DeleteObject(win.HGDIOBJ(hbm))

	return hBitmapToNRGBA(hbm)
}

func hBitmapToNRGBA(hbm win.HBITMAP) (*image.NRGBA, error) {
	var bm win.BITMAP
	if win.GetObject(win.HGDIOBJ(hbm), unsafe.Sizeof(bm), unsafe.Pointer(&bm)) == 0 {
		return nil, fmt.Errorf("GetObject failed for shell thumbnail")
	}

	width := int(bm.BmWidth)
	height := int(math.Abs(float64(bm.BmHeight)))
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("shell thumbnail has invalid size %dx%d", width, height)
	}

	var info win.BITMAPINFO
	info.BmiHeader.BiSize = uint32(unsafe.Sizeof(info.BmiHeader))
	info.BmiHeader.BiWidth = int32(width)
	info.BmiHeader.BiHeight = -int32(height)
	info.BmiHeader.BiPlanes = 1
	info.BmiHeader.BiBitCount = 32
	info.BmiHeader.BiCompression = win.BI_RGB

	raw := make([]byte, width*height*4)
	hdc := win.GetDC(0)
	if hdc == 0 {
		return nil, fmt.Errorf("GetDC failed for shell thumbnail")
	}
	defer win.ReleaseDC(0, hdc)

	if win.GetDIBits(hdc, hbm, 0, uint32(height), &raw[0], &info, win.DIB_RGB_COLORS) == 0 {
		return nil, fmt.Errorf("GetDIBits failed for shell thumbnail")
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < width*height; i++ {
		b := raw[i*4]
		g := raw[i*4+1]
		r := raw[i*4+2]
		a := raw[i*4+3]
		if a == 0 {
			a = 255
		}
		img.Pix[i*4] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = a
	}

	return img, nil
}
