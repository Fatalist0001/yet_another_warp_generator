#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <windows.h>

#include "flutter_window.h"
#include "utils.h"
#include "win32_window.h"

namespace {

// Мьютекс на сессию пользователя: программа живёт в трее, поэтому закрытое
// окно не значит, что процесс завершился. Второй запуск раньше поднимал второй
// системный туннель на том же интерфейсе и ломал маршруты.
constexpr const wchar_t kInstanceMutexName[] =
    L"Local\\WarpgenGui.single-instance";

}  // namespace

int APIENTRY wWinMain(_In_ HINSTANCE instance, _In_opt_ HINSTANCE prev,
                      _In_ wchar_t *command_line, _In_ int show_command) {
  // Attach to console when present (e.g., 'flutter run') or create a
  // new console when running with a debugger.
  if (!::AttachConsole(ATTACH_PARENT_PROCESS) && ::IsDebuggerPresent()) {
    CreateAndAttachConsole();
  }

  HANDLE instance_mutex = ::CreateMutexW(nullptr, TRUE, kInstanceMutexName);
  if (instance_mutex == nullptr) {
    return EXIT_FAILURE;
  }
  if (::GetLastError() == ERROR_ALREADY_EXISTS) {
    // Экземпляр уже работает: показываем его окно и выходим до создания
    // движка. Второй процесс поднял бы второй `warpgen run --tun` с тем же
    // TUN-интерфейсом, файлом остановки и маршрутами.
    HWND running = ::FindWindowW(Win32Window::kWindowClassName, nullptr);
    if (running != nullptr) {
      ::PostMessage(running,
                    ::RegisterWindowMessageW(kRestoreWindowMessageName), 0, 0);
    }
    ::CloseHandle(instance_mutex);
    return EXIT_SUCCESS;
  }

  // Initialize COM, so that it is available for use in the library and/or
  // plugins.
  ::CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

  flutter::DartProject project(L"data");

  std::vector<std::string> command_line_arguments =
      GetCommandLineArguments();

  project.set_dart_entrypoint_arguments(std::move(command_line_arguments));

  FlutterWindow window(project);
  Win32Window::Point origin(10, 10);
  // Размер как у Linux-окна: интерфейс один и тот же (1000x760).
  Win32Window::Size size(1000, 760);
  if (!window.Create(L"Товарищ майор", origin, size)) {
    ::CloseHandle(instance_mutex);
    return EXIT_FAILURE;
  }
  window.SetQuitOnClose(true);

  ::MSG msg;
  while (::GetMessage(&msg, nullptr, 0, 0)) {
    ::TranslateMessage(&msg);
    ::DispatchMessage(&msg);
  }

  ::CoUninitialize();
  ::CloseHandle(instance_mutex);
  return EXIT_SUCCESS;
}
