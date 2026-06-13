#!/usr/bin/env node
"use strict";
// Launcher for the pupptyeer daemon + CLI. Resolves the prebuilt binary from
// the platform package installed as an optional dependency and execs it.
require("../lib/launch.cjs").launch("pupptyeer");
