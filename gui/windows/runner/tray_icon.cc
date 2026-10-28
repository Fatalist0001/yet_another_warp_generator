#include "tray_icon.h"

#include <shellapi.h>

#include <utility>

#include "resource.h"

namespace {

// Идентификатор иконки внутри программы (не ресурс).
constexpr UINT kTrayIconId = 1;
constexpr const wchar_t kTooltip[] = L"Товарищ майор — туннель";

}  // namespace

TrayIcon::~TrayIcon() { Destroy(); }

bool TrayIcon::Create(HWND owner, CommandHandler on_command) {
  if (owner == nullptr) {
    return false;
  }
  owner_ = owner;
  instance_ = ::GetModuleHandleW(nullptr);
  on_command_ = std::move(on_command);
  return AddIcon();
}

bool TrayIcon::AddIcon() {
  if (owner_ == nullptr) {
    return false;
  }

  if (icon_ == nullptr) {
    // Мелкая иконка под размер трея: система масштабирует её по DPI сама.
    // Ресурс живёт столько же, сколько процесс, поэтому DestroyIcon не нужен.
    icon_ = static_cast<HICON>(::LoadImageW(
        instance_, MAKEINTRESOURCEW(IDI_APP_ICON), IMAGE_ICON,
        ::GetSystemMetrics(SM_CXSMICON), ::GetSystemMetrics(SM_CYSMICON),
        LR_DEFAULTCOLOR));
    if (icon_ == nullptr) {
      icon_ = ::LoadIconW(instance_, MAKEINTRESOURCEW(IDI_APP_ICON));
    }
  }

  NOTIFYICONDATAW data = {};
  data.cbSize = sizeof(data);
  data.hWnd = owner_;
  data.uID = kTrayIconId;
  data.uFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP | NIF_SHOWTIP;
  data.uCallbackMessage = kMessage;
  data.hIcon = icon_;
  ::lstrcpynW(data.szTip, kTooltip, ARRAYSIZE(data.szTip));

  if (!::Shell_NotifyIconW(NIM_ADD, &data)) {
    created_ = false;
    return false;
  }

  // Версия 4: контекстное меню по правой кнопке приходит как WM_CONTEXTMENU,
  // а двойной клик — как WM_LBUTTONDBLCLK, без ручного разбора NOTIFYICON_VERSION.
  data.uVersion = NOTIFYICON_VERSION_4;
  ::Shell_NotifyIconW(NIM_SETVERSION, &data);

  created_ = true;
  return true;
}

void TrayIcon::Recreate() {
  // Панель задач после перезапуска explorer.exe пуста: добавляем иконку заново.
  created_ = false;
  AddIcon();
}

void TrayIcon::Destroy() {
  if (!created_ || owner_ == nullptr) {
    created_ = false;
    return;
  }
  NOTIFYICONDATAW data = {};
  data.cbSize = sizeof(data);
  data.hWnd = owner_;
  data.uID = kTrayIconId;
  ::Shell_NotifyIconW(NIM_DELETE, &data);
  created_ = false;
}

void TrayIcon::ShowMenu(POINT point) {
  if (owner_ == nullptr) {
    return;
  }

  HMENU menu = ::CreatePopupMenu();
  if (menu == nullptr) {
    return;
  }
  ::AppendMenuW(menu, MF_STRING, IDM_TRAY_OPEN, L"Открыть");
  ::SetMenuDefaultItem(menu, IDM_TRAY_OPEN, FALSE);
  ::AppendMenuW(menu, MF_STRING, IDM_TRAY_STOP, L"Остановить туннель");
  ::AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);
  ::AppendMenuW(menu, MF_STRING, IDM_TRAY_EXIT, L"Выход");

  // Без SetForegroundWindow меню остаётся на экране после клика мимо него.
  ::SetForegroundWindow(owner_);
  const UINT command =
      ::TrackPopupMenu(menu, TPM_RETURNCMD | TPM_RIGHTBUTTON | TPM_NONOTIFY,
                       point.x, point.y, 0, owner_, nullptr);
  // Документация Shell_NotifyIcon требует «пинок» окна после закрытия меню.
  ::PostMessage(owner_, WM_NULL, 0, 0);
  ::DestroyMenu(menu);

  if (on_command_ == nullptr) {
    return;
  }
  switch (command) {
    case IDM_TRAY_OPEN:
      on_command_(TrayCommand::kOpen);
      break;
    case IDM_TRAY_STOP:
      on_command_(TrayCommand::kStopTunnel);
      break;
    case IDM_TRAY_EXIT:
      on_command_(TrayCommand::kQuit);
      break;
    default:
      break;
  }
}
