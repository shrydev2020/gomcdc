package c0

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

const maxProfileLine = 16 << 20

type profileFileAccumulator struct {
	path   string
	blocks map[SourceRange]ProfileBlock
}

// ParseProfile parses set, count, and atomic Go coverprofile data. Duplicate
// blocks are merged with OR in set mode and addition in count/atomic modes.
func ParseProfile(reader io.Reader) (Profile, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxProfileLine)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Profile{}, fmt.Errorf("parse coverprofile: %w", err)
		}
		return Profile{}, errors.New("parse coverprofile: missing mode line")
	}
	modeLine := strings.TrimSuffix(scanner.Text(), "\r")
	const modePrefix = "mode: "
	if !strings.HasPrefix(modeLine, modePrefix) || len(modeLine) == len(modePrefix) {
		return Profile{}, fmt.Errorf("parse coverprofile line 1: bad mode line %q", modeLine)
	}
	mode := Mode(strings.TrimPrefix(modeLine, modePrefix))
	if !mode.valid() {
		return Profile{}, fmt.Errorf("parse coverprofile line 1: unsupported mode %q", mode)
	}

	files := make(map[string]*profileFileAccumulator)
	lineNumber := 1
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			return Profile{}, fmt.Errorf("parse coverprofile line %d: empty profile line", lineNumber)
		}
		path, block, err := parseProfileLine(line)
		if err != nil {
			return Profile{}, fmt.Errorf("parse coverprofile line %d: %w", lineNumber, err)
		}
		file := files[path]
		if file == nil {
			file = &profileFileAccumulator{path: path, blocks: make(map[SourceRange]ProfileBlock)}
			files[path] = file
		}
		if existing, found := file.blocks[block.Position]; found {
			if existing.Statements != block.Statements {
				return Profile{}, fmt.Errorf(
					"parse coverprofile line %d: inconsistent NumStmt for %s: %d then %d",
					lineNumber,
					formatRange(block.Position),
					existing.Statements,
					block.Statements,
				)
			}
			count, err := mergeCount(mode, existing.Count, block.Count)
			if err != nil {
				return Profile{}, fmt.Errorf("parse coverprofile line %d: %w", lineNumber, err)
			}
			existing.Count = count
			file.blocks[block.Position] = existing
			continue
		}
		file.blocks[block.Position] = block
	}
	if err := scanner.Err(); err != nil {
		return Profile{}, fmt.Errorf("parse coverprofile: %w", err)
	}

	profile := Profile{Mode: mode, Files: make([]ProfileFile, 0, len(files))}
	for _, file := range files {
		profileFile := ProfileFile{Path: file.path, Blocks: make([]ProfileBlock, 0, len(file.blocks))}
		for _, block := range file.blocks {
			profileFile.Blocks = append(profileFile.Blocks, block)
		}
		sort.Slice(profileFile.Blocks, func(i, j int) bool {
			return lessRange(profileFile.Blocks[i].Position, profileFile.Blocks[j].Position)
		})
		profile.Files = append(profile.Files, profileFile)
	}
	sort.Slice(profile.Files, func(i, j int) bool {
		return profile.Files[i].Path < profile.Files[j].Path
	})
	return profile, nil
}

func parseProfileLine(line string) (string, ProfileBlock, error) {
	end := len(line)
	count, end, err := seekBackUint(line, ' ', end, "Count")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	statements, end, err := seekBackUint(line, ' ', end, "NumStmt")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	endColumn, end, err := seekBackUint(line, '.', end, "EndCol")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	endLine, end, err := seekBackUint(line, ',', end, "EndLine")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	startColumn, end, err := seekBackUint(line, '.', end, "StartCol")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	startLine, end, err := seekBackUint(line, ':', end, "StartLine")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	path := line[:end]
	if path == "" {
		return "", ProfileBlock{}, errors.New("file name cannot be blank")
	}

	startLineInt, err := nonnegativeInt(startLine, "StartLine")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	startColumnInt, err := nonnegativeInt(startColumn, "StartCol")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	endLineInt, err := nonnegativeInt(endLine, "EndLine")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	endColumnInt, err := nonnegativeInt(endColumn, "EndCol")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	statementCount, err := nonnegativeInt(statements, "NumStmt")
	if err != nil {
		return "", ProfileBlock{}, err
	}
	rangeValue := SourceRange{
		Start: Position{Line: startLineInt, Column: startColumnInt},
		End:   Position{Line: endLineInt, Column: endColumnInt},
	}
	return path, ProfileBlock{Position: rangeValue, Statements: statementCount, Count: count}, nil
}

func seekBackUint(line string, separator byte, end int, field string) (uint64, int, error) {
	separatorIndex := strings.LastIndexByte(line[:end], separator)
	if separatorIndex < 0 {
		return 0, 0, fmt.Errorf("could not find %q before %s", separator, field)
	}
	literal := line[separatorIndex+1 : end]
	if literal == "" {
		return 0, 0, fmt.Errorf("empty %s", field)
	}
	value, err := strconv.ParseUint(literal, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %s %q: %w", field, literal, err)
	}
	return value, separatorIndex, nil
}

func nonnegativeInt(value uint64, field string) (int, error) {
	if value > uint64(maxInt()) {
		return 0, fmt.Errorf("%s overflows int: %d", field, value)
	}
	return int(value), nil
}

func mergeCount(mode Mode, left, right uint64) (uint64, error) {
	if mode == ModeSet {
		return left | right, nil
	}
	if math.MaxUint64-left < right {
		return 0, fmt.Errorf("coverage count overflow: %d + %d", left, right)
	}
	return left + right, nil
}

func (mode Mode) valid() bool {
	return mode == ModeSet || mode == ModeCount || mode == ModeAtomic
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func lessRange(left, right SourceRange) bool {
	if comparison := comparePosition(left.Start, right.Start); comparison != 0 {
		return comparison < 0
	}
	return comparePosition(left.End, right.End) < 0
}

func comparePosition(left, right Position) int {
	if left.Line < right.Line {
		return -1
	}
	if left.Line > right.Line {
		return 1
	}
	if left.Column < right.Column {
		return -1
	}
	if left.Column > right.Column {
		return 1
	}
	return 0
}

func formatRange(value SourceRange) string {
	return formatPosition(value.Start) + "," + formatPosition(value.End)
}

func formatPosition(value Position) string {
	return fmt.Sprintf("%d.%d", value.Line, value.Column)
}
