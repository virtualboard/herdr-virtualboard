#!/usr/bin/env python3
"""Render monochrome Screen.Draw output captured by e2e/11-terminal-ux.sh.

Optional documentation tooling; requires Pillow, never used by the runtime or CI.
"""
import argparse
import math
import re
import unicodedata
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--columns', type=int, default=140)
    parser.add_argument('--rows', type=int, default=35)
    parser.add_argument('--font', type=Path)
    args = parser.parse_args()
    if not 30 <= args.columns <= 1000 or not 10 <= args.rows <= 500:
        parser.error('use 30–1000 columns and 10–500 rows')
    fonts = [args.font] if args.font else [
        Path('/System/Library/Fonts/Menlo.ttc'),
        Path('/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf'),
    ]
    font_path = next((path for path in fonts if path.is_file()), None)
    if font_path is None:
        parser.error('supply a monospace font with --font PATH')
    font = ImageFont.truetype(str(font_path), 14)
    text = args.input.read_bytes().decode('utf-8', 'replace')
    cols, rows = args.columns, args.rows
    grid = [[' '] * cols for _ in range(rows)]
    row, col, offset = 0, 0, 0
    while offset < len(text):
        char = text[offset]
        if char == '\x1b':
            match = re.match(r'\x1b\[([0-9;?]*)([@-~])', text[offset:])
            if not match:
                offset += 1
                continue
            parameters, command = match.groups()
            offset += len(match[0])
            values = [int(value) if value else 0 for value in parameters.split(';')] if '?' not in parameters else []
            if command in ('H', 'f'):
                row = max(0, min(rows - 1, (values[0] if values and values[0] else 1) - 1))
                col = max(0, min(cols - 1, (values[1] if len(values) > 1 and values[1] else 1) - 1))
            elif command == 'J' and values and values[0] == 2:
                grid = [[' '] * cols for _ in range(rows)]
            elif command == 'K':
                for index in range(col, cols):
                    grid[row][index] = ' '
            # Colour and alternate-buffer/cursor controls do not change the
            # monochrome frame. Captures explicitly use --color=false.
            continue
        offset += 1
        if char == '\r':
            col = 0
            continue
        if char == '\n':
            row = min(rows - 1, row + 1)
            continue
        if ord(char) < 32:
            continue
        if unicodedata.combining(char):
            if col:
                grid[row][col - 1] += char
            continue
        width = 2 if unicodedata.east_asian_width(char) in ('W', 'F') else 1
        if col < cols:
            grid[row][col] = char
            if width == 2 and col + 1 < cols:
                grid[row][col + 1] = ''
        col = min(cols, col + width)
    cell, line = math.ceil(font.getlength('M')), 21
    image = Image.new('RGB', (cols * cell + 32, rows * line + 44), '#101216')
    draw = ImageDraw.Draw(image)
    draw.text((16, 9), 'VirtualBoard · terminal preview', font=font, fill='#8b949e')
    for y, cells in enumerate(grid):
        for x, char in enumerate(cells):
            if char and char != ' ':
                draw.text((16 + x * cell, 34 + y * line), char, font=font, fill='#d6dbe0')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    image.save(args.output)


if __name__ == '__main__':
    main()
