#ifndef RUNNER_TRAY_ICON_H_
#define RUNNER_TRAY_ICON_H_

#include <windows.h>

#include <functional>

// Команда, выбранная в контекстном меню иконки.
enum class TrayCommand {
  kOpen,
  kStopTunnel,
  kQuit,
};

// Иконка в системном трее Windows.
//
// Окно при закрытии сворачивается в трей, а не завершает программу: системный
// туннель продолжает работать, а иконка остаётся единственной видимой точкой
// управления — вернуть окно, остановить туннель или выйти. Без иконки скрытое
// окно выглядело как зависший процесс, и повторный запуск программы поднимал
// второй туннель на том же интерфейсе.
class TrayIcon {
 public:
  using CommandHandler = std::function<void(TrayCommand)>;

  TrayIcon() = default;
  ~TrayIcon();

  TrayIcon(const TrayIcon&) = delete;
  TrayIcon& operator=(const TrayIcon&) = delete;

  // Создаёт иконку и привязывает её к окну |owner|.
  bool Create(HWND owner, CommandHandler on_command);

  // Убирает иконку. Повторный вызов безопасен.
  void Destroy();

  // Добавляет иконку заново после перезапуска explorer.exe: панель задач не
  // помнит иконок предыдущего процесса.
  void Recreate();

  // Сообщение, которым иконка уведомляет окно о кликах и меню.
  static constexpr UINT kMessage = WM_APP + 1;

  // Показывает контекстное меню в точке экрана |point|.
  void ShowMenu(POINT point);

  bool created() const { return created_; }

 private:
  bool AddIcon();

  HWND owner_ = nullptr;
  HINSTANCE instance_ = nullptr;
  HICON icon_ = nullptr;
  CommandHandler on_command_;
  bool created_ = false;
};

#endif  // RUNNER_TRAY_ICON_H_
