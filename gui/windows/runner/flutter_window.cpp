#include "flutter_window.h"

#include <flutter/standard_method_codec.h>
#include <windowsx.h>

#include <optional>

#include "tray_icon.h"
#include "utils.h"

#include "flutter/generated_plugin_registrant.h"

namespace {

// Канал, по которому Dart и runner договариваются о трее.
constexpr char kTrayChannelName[] = "ru.vernette.warpgen/tray";

// Таймер страховки: если Dart не ответил на «Выход», закрываемся сами.
constexpr UINT_PTR kExitTimerId = 0x5747;
constexpr UINT kExitTimeoutMs = 15000;

}  // namespace

const wchar_t kRestoreWindowMessageName[] = L"WarpgenGuiRestoreWindow";

FlutterWindow::FlutterWindow(const flutter::DartProject& project)
    : project_(project) {}

FlutterWindow::~FlutterWindow() { tray_.Destroy(); }

bool FlutterWindow::OnCreate() {
  if (!Win32Window::OnCreate()) {
    return false;
  }

  RECT frame = GetClientArea();

  // The size here must match the window dimensions to avoid unnecessary surface
  // creation / destruction in the startup path.
  flutter_controller_ = std::make_unique<flutter::FlutterViewController>(
      frame.right - frame.left, frame.bottom - frame.top, project_);
  // Ensure that basic setup of the controller was successful.
  if (!flutter_controller_->engine() || !flutter_controller_->view()) {
    return false;
  }
  RegisterPlugins(flutter_controller_->engine());
  SetChildContent(flutter_controller_->view()->GetNativeWindow());

  CreateTrayChannel();

  flutter_controller_->engine()->SetNextFrameCallback([&]() {
    this->Show();
  });

  // Flutter can complete the first frame before the "show window" callback is
  // registered. The following call ensures a frame is pending to ensure the
  // window is shown. It is a no-op if the first frame hasn't completed yet.
  flutter_controller_->ForceRedraw();

  restore_window_message_ = ::RegisterWindowMessageW(kRestoreWindowMessageName);
  taskbar_created_message_ = ::RegisterWindowMessageW(L"TaskbarCreated");

  // Иконка создаётся сразу: окно может быть свёрнуто в трей в любой момент,
  // и без иконки скрытое окно уже не вернуть.
  tray_.Create(GetHandle(), [this](TrayCommand command) {
    HandleTrayCommand(command);
  });

  return true;
}

void FlutterWindow::CreateTrayChannel() {
  if (!flutter_controller_ || !flutter_controller_->engine()) {
    return;
  }
  tray_channel_ =
      std::make_unique<flutter::MethodChannel<flutter::EncodableValue>>(
          flutter_controller_->engine()->messenger(), kTrayChannelName,
          &flutter::StandardMethodCodec::GetInstance());

  tray_channel_->SetMethodCallHandler(
      [this](const flutter::MethodCall<flutter::EncodableValue>& call,
             std::unique_ptr<flutter::MethodResult<flutter::EncodableValue>>
                 result) {
        if (call.method_name() == "minimizeToTray") {
          MinimizeToTray();
          result->Success();
          return;
        }
        if (call.method_name() == "restoreFromTray") {
          RestoreFromWindow();
          result->Success();
          return;
        }
        if (call.method_name() == "quit") {
          // Туннель Dart уже остановил: закрываем окно штатно, чтобы снялись
          // маршруты и завершились дочерние процессы. exit(0) здесь оставил бы
          // применённые маршруты до перезагрузки.
          result->Success();
          exit_pending_ = true;
          HWND window = GetHandle();
          if (window != nullptr) {
            ::PostMessage(window, WM_CLOSE, 0, 0);
          }
          return;
        }
        result->NotImplemented();
      });
}

void FlutterWindow::HandleTrayCommand(TrayCommand command) {
  switch (command) {
    case TrayCommand::kOpen:
      RestoreFromWindow();
      break;
    case TrayCommand::kStopTunnel:
      // Окно остаётся открытым: остановка идёт обычным путём, со статусом.
      if (tray_channel_) {
        tray_channel_->InvokeMethod("trayStopTunnel", nullptr);
      }
      break;
    case TrayCommand::kQuit:
      if (quit_requested_ || exit_pending_) {
        break;
      }
      quit_requested_ = true;
      // Сначала Dart останавливает туннель и отвечает `quit`, затем окно
      // закрывается. Таймер — страховка от зависшей остановки.
      if (tray_channel_) {
        tray_channel_->InvokeMethod("trayQuit", nullptr);
      }
      ForceExitAfterTimeout();
      break;
  }
}

void FlutterWindow::MinimizeToTray() {
  HWND window = GetHandle();
  if (window == nullptr) {
    return;
  }
  ::ShowWindow(window, SW_HIDE);
}

void FlutterWindow::RestoreFromWindow() {
  HWND window = GetHandle();
  if (window == nullptr) {
    return;
  }
  ::ShowWindow(window, SW_SHOW);
  ::ShowWindow(window, SW_RESTORE);
  ::SetForegroundWindow(window);
}

void FlutterWindow::ForceExitAfterTimeout() {
  HWND window = GetHandle();
  if (window == nullptr) {
    return;
  }
  ::SetTimer(window, kExitTimerId, kExitTimeoutMs, nullptr);
}

void FlutterWindow::OnDestroy() {
  tray_.Destroy();

  // Канал держит указатель на messenger движка: снимаем обработчик и
  // уничтожаем канал до остановки самого движка.
  if (tray_channel_) {
    tray_channel_->SetMethodCallHandler(nullptr);
    tray_channel_.reset();
  }

  if (flutter_controller_) {
    flutter_controller_ = nullptr;
  }

  Win32Window::OnDestroy();
}

LRESULT
FlutterWindow::MessageHandler(HWND hwnd, UINT const message,
                              WPARAM const wparam,
                              LPARAM const lparam) noexcept {
  if (restore_window_message_ != 0 && message == restore_window_message_) {
    RestoreFromWindow();
    return 0;
  }
  if (taskbar_created_message_ != 0 && message == taskbar_created_message_) {
    tray_.Recreate();
    return 0;
  }

  switch (message) {
    case WM_TIMER:
      if (wparam == kExitTimerId) {
        ::KillTimer(hwnd, kExitTimerId);
        if (!exit_pending_) {
          exit_pending_ = true;
          ::PostMessage(hwnd, WM_CLOSE, 0, 0);
        }
        return 0;
      }
      break;

    case WM_CLOSE:
      // Крестик сворачивает в трей: системный туннель продолжает работать, а
      // вернуть окно можно кликом по иконке или пунктом «Открыть».
      if (exit_pending_) {
        break;
      }
      // Иконки нет (создать не удалось): свернуть окно значило бы потерять
      // программу совсем — закрываемся штатно, как обычное приложение.
      if (!tray_.created()) {
        break;
      }
      MinimizeToTray();
      return 0;

    case TrayIcon::kMessage: {
      // NOTIFYICON_VERSION_4 (его выставляет TrayIcon) упаковывает событие в
      // LOWORD(lParam), а идентификатор иконки — в HIWORD. Пока код сравнивал
      // lParam целиком, ни один клик не совпадал с событием: иконка в трее не
      // реагировала ни на ЛКМ, ни на ПКМ, и программу нельзя было закрыть.
      const UINT notification = LOWORD(static_cast<DWORD_PTR>(lparam));
      switch (notification) {
        case WM_CONTEXTMENU:
        case WM_RBUTTONUP: {
          // В версии 4 координаты клика лежат в wParam; в старых версиях их
          // там нет, поэтому берём позицию курсора.
          POINT point = {};
          if (notification == WM_CONTEXTMENU) {
            point = {GET_X_LPARAM(wparam), GET_Y_LPARAM(wparam)};
          } else {
            ::GetCursorPos(&point);
          }
          tray_.ShowMenu(point);
          return 0;
        }
        case NIN_SELECT:
        case NIN_KEYSELECT:
        case WM_LBUTTONUP:
        case WM_LBUTTONDBLCLK:
          // Левый клик возвращает окно: скрытое окно иначе не достать.
          RestoreFromWindow();
          return 0;
        default:
          return 0;
      }
    }

    default:
      break;
  }

  // Give Flutter, including plugins, an opportunity to handle window messages.
  if (flutter_controller_) {
    std::optional<LRESULT> result =
        flutter_controller_->HandleTopLevelWindowProc(hwnd, message, wparam,
                                                      lparam);
    if (result) {
      return *result;
    }
  }

  switch (message) {
    case WM_FONTCHANGE:
      if (flutter_controller_) {
        flutter_controller_->engine()->ReloadSystemFonts();
      }
      break;
    default:
      break;
  }

  return Win32Window::MessageHandler(hwnd, message, wparam, lparam);
}
