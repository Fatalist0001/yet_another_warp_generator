#ifndef RUNNER_FLUTTER_WINDOW_H_
#define RUNNER_FLUTTER_WINDOW_H_

#include <flutter/dart_project.h>
#include <flutter/encodable_value.h>
#include <flutter/flutter_view_controller.h>
#include <flutter/method_channel.h>

#include <memory>

#include "tray_icon.h"
#include "win32_window.h"

// Сообщение «покажи окно»: его посылает второй запуск уже работающему
// экземпляру, чтобы не поднимать второй туннель.
extern const wchar_t kRestoreWindowMessageName[];

// A window that does nothing but host a Flutter view.
class FlutterWindow : public Win32Window {
 public:
  // Creates a new FlutterWindow hosting a Flutter view running |project|.
  explicit FlutterWindow(const flutter::DartProject& project);
  virtual ~FlutterWindow();

 protected:
  // Win32Window:
  bool OnCreate() override;
  void OnDestroy() override;
  LRESULT MessageHandler(HWND window, UINT const message, WPARAM const wparam,
                         LPARAM const lparam) noexcept override;

 private:
  // Канал трея: меню в runner'е, а остановка туннеля и выход — в Dart.
  void CreateTrayChannel();

  // Выполняет команду меню трея.
  void HandleTrayCommand(TrayCommand command);

  // Свернуть окно в трей и вернуть его обратно.
  void MinimizeToTray();
  void RestoreFromWindow();

  // Страховка: если Dart не ответил на «Выход», закрываемся сами, иначе
  // иконка останется в трее, а процесс — висеть без окна.
  void ForceExitAfterTimeout();

  // The project to run.
  flutter::DartProject project_;

  // The Flutter instance hosted by this window.
  std::unique_ptr<flutter::FlutterViewController> flutter_controller_;

  // Канал трея; создаётся в OnCreate вместе с движком.
  std::unique_ptr<flutter::MethodChannel<flutter::EncodableValue>>
      tray_channel_;

  // Иконка в трее.
  TrayIcon tray_;

  // В трее нажали «Выход»: ждём, пока Dart остановит туннель.
  bool quit_requested_ = false;

  // Окно закрывается окончательно (Dart уже всё остановил или сработал таймер).
  bool exit_pending_ = false;

  UINT restore_window_message_ = 0;
  UINT taskbar_created_message_ = 0;
};

#endif  // RUNNER_FLUTTER_WINDOW_H_
