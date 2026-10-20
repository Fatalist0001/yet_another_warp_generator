// GUI warpgen: Material You-интерфейс к локальному CLI.
//
// Приложение не вшивает ядро: оно запускает бинарник `warpgen` и читает его
// машинный вывод (`--json`). Туннель остаётся userspace, root не нужен.

import 'package:flutter/material.dart';

import 'src/app.dart';

void main() {
  runApp(const WarpgenApp());
}
