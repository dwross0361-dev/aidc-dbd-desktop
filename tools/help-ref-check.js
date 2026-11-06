#!/usr/bin/env node
// Fails (exit 1) when the user guide's reference differs from what the builder says. Run before a release, with the neutrality gate.
'use strict';
process.argv.push('--check');
require('./gen-help-ref.js');
