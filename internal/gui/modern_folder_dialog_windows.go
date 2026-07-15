//go:build windows

package gui

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

const (
	fileDialogOptionsPickFolders     = 0x00000020
	fileDialogOptionsForceFileSystem = 0x00000040
	fileDialogOptionsPathMustExist   = 0x00000800

	sigdnFileSystemPath = 0x80058000

	hresultCancelled = 0x800704C7
)

var (
	clsidFileOpenDialog = win.CLSID{
		Data1: 0xdc1c5a9c,
		Data2: 0xe88a,
		Data3: 0x4dde,
		Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7},
	}
	iidFileOpenDialog = win.IID{
		Data1: 0xd57c7288,
		Data2: 0xd4ad,
		Data3: 0x4768,
		Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60},
	}
	iidShellItem = win.IID{
		Data1: 0x43826d1e,
		Data2: 0xe718,
		Data3: 0x42ee,
		Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe},
	}
)

type modernFileOpenDialog struct {
	LpVtbl *modernFileOpenDialogVtbl
}

type modernFileOpenDialogVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	Show           uintptr

	SetFileTypes     uintptr
	SetFileTypeIndex uintptr
	GetFileTypeIndex uintptr
	Advise           uintptr
	Unadvise         uintptr
	SetOptions       uintptr
	GetOptions       uintptr
	SetDefaultFolder uintptr
	SetFolder        uintptr
	GetFolder        uintptr
	GetCurrentSel    uintptr
	SetFileName      uintptr
	GetFileName      uintptr
	SetTitle         uintptr
	SetOkButtonLabel uintptr
	SetFileNameLabel uintptr
	GetResult        uintptr
	AddPlace         uintptr
	SetDefaultExt    uintptr
	Close            uintptr
	SetClientGuid    uintptr
	ClearClientData  uintptr
	SetFilter        uintptr
	GetResults       uintptr
	GetSelectedItems uintptr
}

type modernShellItem struct {
	LpVtbl *modernShellItemVtbl
}

type modernShellItemVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
	GetAttributes  uintptr
	Compare        uintptr
}

func showModernFolderDialog(owner walk.Form, title string, initialDir string) (string, bool, error) {
	if hr := win.OleInitialize(); hr != win.S_OK && hr != win.S_FALSE {
		return "", false, fmt.Errorf("OleInitialize failed: HRESULT 0x%08x", uint32(hr))
	}
	defer win.OleUninitialize()

	var dialogPtr unsafe.Pointer
	hr := win.CoCreateInstance(&clsidFileOpenDialog, nil, win.CLSCTX_INPROC_SERVER, &iidFileOpenDialog, &dialogPtr)
	if win.FAILED(hr) || dialogPtr == nil {
		return "", false, fmt.Errorf("modern folder picker unavailable: HRESULT 0x%08x", uint32(hr))
	}

	dialog := (*modernFileOpenDialog)(dialogPtr)
	defer dialog.release()

	var options uint32
	if hr := dialog.getOptions(&options); win.FAILED(hr) {
		return "", false, fmt.Errorf("folder picker options failed: HRESULT 0x%08x", uint32(hr))
	}
	options |= fileDialogOptionsPickFolders | fileDialogOptionsForceFileSystem | fileDialogOptionsPathMustExist
	if hr := dialog.setOptions(options); win.FAILED(hr) {
		return "", false, fmt.Errorf("folder picker folder mode failed: HRESULT 0x%08x", uint32(hr))
	}

	if err := dialog.setTitle(title); err != nil {
		return "", false, err
	}
	if err := dialog.setOKButtonLabel("Select Folder"); err != nil {
		return "", false, err
	}
	if err := dialog.setInitialFolder(initialDir); err != nil {
		return "", false, err
	}

	var ownerHwnd win.HWND
	if owner != nil {
		ownerHwnd = owner.Handle()
	}

	hr = dialog.show(ownerHwnd)
	if uint32(hr) == hresultCancelled {
		return "", false, nil
	}
	if win.FAILED(hr) {
		return "", false, fmt.Errorf("folder picker failed: HRESULT 0x%08x", uint32(hr))
	}

	item, hr := dialog.getResult()
	if win.FAILED(hr) || item == nil {
		return "", false, fmt.Errorf("folder picker result failed: HRESULT 0x%08x", uint32(hr))
	}
	defer item.release()

	path, err := item.fileSystemPath()
	if err != nil {
		return "", false, err
	}
	return path, path != "", nil
}

func (d *modernFileOpenDialog) release() {
	if d == nil || d.LpVtbl == nil {
		return
	}
	_, _, _ = syscall.SyscallN(d.LpVtbl.Release, uintptr(unsafe.Pointer(d)))
}

func (d *modernFileOpenDialog) show(owner win.HWND) win.HRESULT {
	ret, _, _ := syscall.SyscallN(d.LpVtbl.Show, uintptr(unsafe.Pointer(d)), uintptr(owner))
	return win.HRESULT(ret)
}

func (d *modernFileOpenDialog) getOptions(options *uint32) win.HRESULT {
	ret, _, _ := syscall.SyscallN(d.LpVtbl.GetOptions, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(options)))
	return win.HRESULT(ret)
}

func (d *modernFileOpenDialog) setOptions(options uint32) win.HRESULT {
	ret, _, _ := syscall.SyscallN(d.LpVtbl.SetOptions, uintptr(unsafe.Pointer(d)), uintptr(options))
	return win.HRESULT(ret)
}

func (d *modernFileOpenDialog) setTitle(title string) error {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	ret, _, _ := syscall.SyscallN(d.LpVtbl.SetTitle, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(titlePtr)))
	if hr := win.HRESULT(ret); win.FAILED(hr) {
		return fmt.Errorf("folder picker title failed: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

func (d *modernFileOpenDialog) setOKButtonLabel(label string) error {
	labelPtr, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return err
	}
	ret, _, _ := syscall.SyscallN(d.LpVtbl.SetOkButtonLabel, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(labelPtr)))
	if hr := win.HRESULT(ret); win.FAILED(hr) {
		return fmt.Errorf("folder picker button label failed: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

func (d *modernFileOpenDialog) setInitialFolder(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil
	}

	item, err := shellItemFromPath(path)
	if err != nil {
		return nil
	}
	defer item.release()

	ret, _, _ := syscall.SyscallN(d.LpVtbl.SetFolder, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(item)))
	if hr := win.HRESULT(ret); win.FAILED(hr) {
		return nil
	}
	return nil
}

func (d *modernFileOpenDialog) getResult() (*modernShellItem, win.HRESULT) {
	var item *modernShellItem
	ret, _, _ := syscall.SyscallN(d.LpVtbl.GetResult, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&item)))
	return item, win.HRESULT(ret)
}

func shellItemFromPath(path string) (*modernShellItem, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	var item *modernShellItem
	hrRaw, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(unsafe.Pointer(&iidShellItem)),
		uintptr(unsafe.Pointer(&item)),
	)
	hr := win.HRESULT(hrRaw)
	if win.FAILED(hr) || item == nil {
		return nil, fmt.Errorf("shell item creation failed: HRESULT 0x%08x", uint32(hr))
	}
	return item, nil
}

func (s *modernShellItem) release() {
	if s == nil || s.LpVtbl == nil {
		return
	}
	_, _, _ = syscall.SyscallN(s.LpVtbl.Release, uintptr(unsafe.Pointer(s)))
}

func (s *modernShellItem) fileSystemPath() (string, error) {
	var pathPtr *uint16
	ret, _, _ := syscall.SyscallN(s.LpVtbl.GetDisplayName, uintptr(unsafe.Pointer(s)), sigdnFileSystemPath, uintptr(unsafe.Pointer(&pathPtr)))
	if hr := win.HRESULT(ret); win.FAILED(hr) || pathPtr == nil {
		return "", fmt.Errorf("folder path lookup failed: HRESULT 0x%08x", uint32(hr))
	}
	defer win.CoTaskMemFree(uintptr(unsafe.Pointer(pathPtr)))

	return utf16PtrToString(pathPtr), nil
}

func utf16PtrToString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}

	length := 0
	for {
		value := *(*uint16)(unsafe.Pointer(uintptr(unsafe.Pointer(ptr)) + uintptr(length*2)))
		if value == 0 {
			break
		}
		length++
	}

	return syscall.UTF16ToString(unsafe.Slice(ptr, length))
}
