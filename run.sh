#!/bin/bash
/backplane connect &
exec bundle exec puma -C config/puma.rb
