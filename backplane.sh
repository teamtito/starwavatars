#!/bin/bash
/backplane connect &
exec "$@"
