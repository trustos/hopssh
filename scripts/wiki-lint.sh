#!/usr/bin/env bash
# dev-wiki/lint.sh — wiki health checks. Deterministic, no LLM dependency.
#
# Usage (run from project root, or pass --wiki):
#   bash scripts/wiki-lint.sh                       # alias for --drift
#   bash scripts/wiki-lint.sh --drift
#   bash scripts/wiki-lint.sh --orphans
#   bash scripts/wiki-lint.sh --stale
#   bash scripts/wiki-lint.sh --duplicates
#   bash scripts/wiki-lint.sh --missing-sources
#   bash scripts/wiki-lint.sh --missing-decision-basis
#   bash scripts/wiki-lint.sh --blast-radius PAGE
#   bash scripts/wiki-lint.sh --all
#
# Exit code: 0 if clean, 1 if any check found problems.

set -uo pipefail

WIKI=""
MODE=""
BLAST_TARGET=""
STALE_DAYS=90

while [[ $# -gt 0 ]]; do
    case "$1" in
        --wiki) WIKI="$2"; shift 2 ;;
        --wiki=*) WIKI="${1#*=}"; shift ;;
        --drift) MODE="drift"; shift ;;
        --orphans) MODE="orphans"; shift ;;
        --stale) MODE="stale"; shift ;;
        --duplicates) MODE="duplicates"; shift ;;
        --missing-sources) MODE="missing-sources"; shift ;;
        --missing-decision-basis) MODE="missing-decision-basis"; shift ;;
        --blast-radius) MODE="blast"; BLAST_TARGET="$2"; shift 2 ;;
        --all) MODE="all"; shift ;;
        --stale-days) STALE_DAYS="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,17p' "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *) echo "lint.sh: unknown arg: $1" >&2; exit 2 ;;
    esac
done

[[ -z "$MODE" ]] && MODE="drift"

# Resolve wiki dir.
if [[ -z "$WIKI" ]]; then
    if [[ -d "docs/wiki" ]]; then
        WIKI="docs/wiki"
    else
        # Walk up looking for docs/wiki.
        d="$(pwd)"
        while [[ "$d" != "/" ]]; do
            if [[ -d "$d/docs/wiki" ]]; then WIKI="$d/docs/wiki"; break; fi
            d="$(dirname "$d")"
        done
    fi
fi

if [[ -z "$WIKI" ]] || [[ ! -d "$WIKI" ]]; then
    echo "lint.sh: docs/wiki/ not found. Pass --wiki PATH." >&2
    exit 2
fi

PROJECT_ROOT="$(cd "$WIKI/../.." && pwd)"
EXIT=0

#------------------------------------------------------------------------------
# DRIFT — wiki references files/tags that no longer exist.
#------------------------------------------------------------------------------
check_drift() {
    echo "== drift =="
    local found=0

    # Meta files that document the format itself — they contain example backlinks
    # like [[wiki-page-name]] and version strings used as documentation, NOT real
    # claims about the project. Exclude from drift checks.
    local exclude_args=(
        --exclude=SCHEMA.md
        --exclude=index.md
        --exclude=log.md
        --exclude=open-questions.md
        --exclude=USAGE.md
        --exclude=.framework-version
    )

    # 1. Source-tree path references that point to nonexistent files.
    # Match common project layouts: cmd/X, internal/X, src/X, lib/X, frontend/X, scripts/X
    local pattern='(cmd|internal|pkg|src|lib|frontend|app|scripts|clients|services|api)/[A-Za-z0-9_./-]+\.(go|ts|tsx|js|jsx|py|rs|sh|md|sql|svelte)'
    local refs
    # Exclude raw/ — those are external clippings, not project claims.
    refs=$(grep -rhoE "${exclude_args[@]}" --exclude-dir=raw "$pattern" "$WIKI" 2>/dev/null | sort -u || true)
    while IFS= read -r ref; do
        [[ -z "$ref" ]] && continue
        if [[ -e "$PROJECT_ROOT/$ref" ]]; then continue; fi
        # Maybe extracted as a path-suffix (e.g. wiki cites `clients/.../src/lib.rs`,
        # the regex captured just `src/lib.rs`). Try locating any file under the
        # project whose path ends with this suffix.
        if find "$PROJECT_ROOT" -type d \( -name node_modules -o -name vendor -o -name .git -o -name target -o -name dist -o -name build \) -prune -o -type f -path "*/$ref" -print 2>/dev/null | head -1 | grep -q .; then
            continue
        fi
        local where
        where=$(grep -rl --exclude-dir=raw "$ref" "$WIKI" 2>/dev/null | head -3 | sed "s|^$WIKI/||" | tr '\n' ',' | sed 's/,$//')
        echo "  DRIFT: missing file '$ref' (referenced in: $where)"
        found=1
    done <<< "$refs"

    # 2. Backlink references to nonexistent pages.
    local backlinks
    backlinks=$(grep -rhoE "${exclude_args[@]}" --exclude-dir=raw '\[\[[A-Za-z0-9_./-]+\]\]' "$WIKI" 2>/dev/null | sort -u | sed 's/^\[\[//;s/\]\]$//' || true)
    while IFS= read -r link; do
        [[ -z "$link" ]] && continue
        # Strip leading "../" path traversal noise.
        local cleaned="${link#../}"
        local name="$cleaned"
        name="${name##*/}"  # last path segment
        # Search anywhere under wiki for a file that matches.
        if ! find "$WIKI" -type f \( -name "${name}.md" -o -name "${name}" \) 2>/dev/null | grep -q .; then
            local where
            where=$(grep -rl --exclude-dir=raw "\[\[$link\]\]" "$WIKI" 2>/dev/null | head -3 | sed "s|^$WIKI/||" | tr '\n' ',' | sed 's/,$//')
            echo "  DRIFT: backlink to missing page '[[$link]]' (referenced in: $where)"
            found=1
        fi
    done <<< "$backlinks"

    # 3. Version-tag references — `vX.Y.Z` mentioned but no matching git tag.
    if git -C "$PROJECT_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
        local version_refs
        version_refs=$(grep -rhoE "${exclude_args[@]}" 'v[0-9]+\.[0-9]+\.[0-9]+' "$WIKI" 2>/dev/null | sort -u || true)
        local existing_tags
        existing_tags=$(git -C "$PROJECT_ROOT" tag -l 2>/dev/null || true)
        while IFS= read -r ver; do
            [[ -z "$ver" ]] && continue
            if ! grep -qx "$ver" <<< "$existing_tags"; then
                # Only flag if it's claimed-as-shipped (has frontmatter status: shipped or appears next to "shipped").
                local where
                where=$(grep -rl "${exclude_args[@]}" "$ver" "$WIKI" 2>/dev/null | head -3 | sed "s|^$WIKI/||" | tr '\n' ',' | sed 's/,$//')
                echo "  DRIFT: version '$ver' has no matching git tag (referenced in: $where)"
                found=1
            fi
        done <<< "$version_refs"
    fi

    if [[ $found == 0 ]]; then
        echo "  ok"
    else
        EXIT=1
    fi
}

#------------------------------------------------------------------------------
# ORPHANS — pages exist but nothing links to them.
#------------------------------------------------------------------------------
check_orphans() {
    echo "== orphans =="
    local found=0

    local all_pages
    all_pages=$(find "$WIKI" -type f -name '*.md' \
        ! -name 'SCHEMA.md' ! -name 'index.md' ! -name 'log.md' \
        ! -name 'open-questions.md' ! -name 'USAGE.md' \
        -not -path "$WIKI/raw/*" 2>/dev/null | sort)

    while IFS= read -r page; do
        [[ -z "$page" ]] && continue
        local rel="${page#$WIKI/}"
        local stem
        stem=$(basename "$page" .md)
        # Path-with-category form, e.g. decisions/phase-s-renewal-ticker.
        local rel_no_md="${rel%.md}"
        # Linked from index.md? Or from any other page?
        # Match either [[stem]] or [[category/stem]] or relative path.
        local linked=0
        if grep -qE "\[\[($stem|$rel_no_md|\.\./$rel_no_md)\]\]" "$WIKI/index.md" 2>/dev/null; then linked=1; fi
        if [[ $linked == 0 ]] && grep -q "$rel" "$WIKI/index.md" 2>/dev/null; then linked=1; fi
        if [[ $linked == 0 ]]; then
            if grep -rlE --exclude-dir=raw "\[\[($stem|$rel_no_md|\.\./$rel_no_md)\]\]" "$WIKI" 2>/dev/null | grep -v "^$page$" | grep -q .; then
                linked=1
            fi
        fi
        if [[ $linked == 0 ]]; then
            echo "  ORPHAN: $rel"
            found=1
        fi
    done <<< "$all_pages"

    if [[ $found == 0 ]]; then echo "  ok"; else EXIT=1; fi
}

#------------------------------------------------------------------------------
# STALE — pages with last_compiled older than $STALE_DAYS and status: current.
#------------------------------------------------------------------------------
check_stale() {
    echo "== stale (>$STALE_DAYS days, status: current) =="
    local found=0
    local cutoff_epoch
    if date -v-"${STALE_DAYS}"d +%s >/dev/null 2>&1; then
        # macOS date
        cutoff_epoch=$(date -v-"${STALE_DAYS}"d +%s)
    else
        # GNU date
        cutoff_epoch=$(date -d "${STALE_DAYS} days ago" +%s)
    fi

    while IFS= read -r page; do
        [[ -z "$page" ]] && continue
        # Skip meta files that don't carry status/last_compiled.
        case "$(basename "$page")" in
            SCHEMA.md|index.md|log.md|open-questions.md|USAGE.md|.framework-version) continue ;;
        esac
        # Extract last_compiled from frontmatter (first 20 lines).
        local lc
        lc=$(head -20 "$page" | grep -E '^last_compiled:' | head -1 | sed 's/^last_compiled:[[:space:]]*//;s/[[:space:]]*$//')
        [[ -z "$lc" ]] && continue
        local status
        status=$(head -20 "$page" | grep -E '^status:' | head -1 | sed 's/^status:[[:space:]]*//;s/[[:space:]]*$//')
        [[ "$status" != "current" ]] && continue
        local lc_epoch
        lc_epoch=$(date -j -f "%Y-%m-%d" "$lc" +%s 2>/dev/null || date -d "$lc" +%s 2>/dev/null || echo 0)
        if [[ "$lc_epoch" -gt 0 ]] && [[ "$lc_epoch" -lt "$cutoff_epoch" ]]; then
            echo "  STALE: ${page#$WIKI/} (last_compiled: $lc, status: current)"
            found=1
        fi
    done < <(find "$WIKI" -type f -name '*.md' -not -path "$WIKI/raw/*")

    if [[ $found == 0 ]]; then echo "  ok"; else EXIT=1; fi
}

#------------------------------------------------------------------------------
# DUPLICATES — pages with very similar first paragraphs.
#------------------------------------------------------------------------------
check_duplicates() {
    echo "== duplicates =="
    local found=0
    local tmp
    tmp=$(mktemp)
    # For each non-special page: hash the first content paragraph after frontmatter.
    while IFS= read -r page; do
        [[ -z "$page" ]] && continue
        # Skip frontmatter (between first two lines of '---'), then take first non-empty body line.
        local body
        body=$(awk '
            BEGIN{infm=0; done=0}
            /^---$/ {if (infm==0){infm=1; next} else {infm=2; next}}
            infm==2 && NF>0 && !done {print tolower($0); done=1}
        ' "$page" | tr -dc 'a-z0-9 ' | tr -s ' ')
        [[ -z "$body" ]] && continue
        local hash
        hash=$(echo "$body" | shasum -a 1 2>/dev/null | awk '{print $1}')
        [[ -z "$hash" ]] && hash=$(echo "$body" | sha1sum 2>/dev/null | awk '{print $1}')
        echo "$hash ${page#$WIKI/}" >> "$tmp"
    done < <(find "$WIKI" -type f -name '*.md' \
        ! -name 'SCHEMA.md' ! -name 'index.md' ! -name 'log.md' \
        -not -path "$WIKI/raw/*")

    # Group by hash, flag any group with >1 entry.
    sort "$tmp" | awk '{
        if ($1==prev) {grp = grp"," $2}
        else {if (count>1) print prev": "grp; prev=$1; grp=$2; count=1; next}
        count++
    }
    END {if (count>1) print prev": "grp}' | while IFS= read -r line; do
        if [[ -n "$line" ]]; then
            echo "  DUP: ${line#*: }"
            found=1
            EXIT=1
        fi
    done
    rm -f "$tmp"
    if [[ $found == 0 ]]; then echo "  ok"; fi
}

#------------------------------------------------------------------------------
# MISSING-SOURCES — pages without sources frontmatter AND no prior-knowledge marker.
#------------------------------------------------------------------------------
check_missing_sources() {
    echo "== missing-sources =="
    local found=0
    while IFS= read -r page; do
        [[ -z "$page" ]] && continue
        # Skip meta files that don't carry sources frontmatter.
        case "$(basename "$page")" in
            SCHEMA.md|index.md|log.md|open-questions.md|USAGE.md|.framework-version) continue ;;
        esac
        # Extract frontmatter block (between first two `---` markers) and check
        # whether `sources:` has any non-empty list items beneath it.
        local has_sources=0
        has_sources=$(awk '
            BEGIN{in_fm=0; in_sources=0; count=0}
            /^---$/ {if (in_fm==0) {in_fm=1; next} else {exit}}
            in_fm==1 && /^sources:[[:space:]]*$/ {in_sources=1; next}
            in_fm==1 && /^[a-z]/ {in_sources=0}
            in_sources==1 && /^[[:space:]]+-[[:space:]]+[^[:space:]]/ {count++}
            END {print count}
        ' "$page")
        if [[ "$has_sources" -gt 0 ]]; then continue; fi
        # No sources entries — does the page carry a prior-knowledge marker?
        if grep -qE '> Reasoned from training-data priors' "$page" 2>/dev/null; then
            continue
        fi
        echo "  MISSING-SOURCES: ${page#$WIKI/}"
        found=1
    done < <(find "$WIKI" -type f -name '*.md' -not -path "$WIKI/raw/*")
    if [[ $found == 0 ]]; then echo "  ok"; else EXIT=1; fi
}

#------------------------------------------------------------------------------
# MISSING-DECISION-BASIS — ADR pages without a populated `## Decision basis`.
#------------------------------------------------------------------------------
check_missing_decision_basis() {
    echo "== missing-decision-basis =="
    local found=0
    local decisions_dir="$WIKI/decisions"
    [[ ! -d "$decisions_dir" ]] && { echo "  (no decisions/ directory yet)"; return; }
    while IFS= read -r page; do
        [[ -z "$page" ]] && continue
        # Verify the page is type: decision (frontmatter check).
        local is_decision
        is_decision=$(awk '
            BEGIN{in_fm=0}
            /^---$/ {if (in_fm==0) {in_fm=1; next} else {exit}}
            in_fm==1 && /^type:[[:space:]]+decision/ {print "yes"; exit}
        ' "$page")
        [[ "$is_decision" != "yes" ]] && continue
        # Check for `## Decision basis` heading + at least one populated bullet.
        # A populated bullet: line starting with `- **<label>:**` followed by content
        # that is NOT a placeholder ([…], or empty after the colon).
        local populated
        populated=$(awk '
            BEGIN{in_section=0; populated=0}
            /^## / {if ($0 ~ /^## Decision basis/) {in_section=1; next} else if (in_section) {exit}}
            in_section==1 && /^[[:space:]]*-[[:space:]]+\*\*[A-Za-z]/ {
                # Extract content after the **label:** marker.
                line=$0
                sub(/.*\*\*[^*]+\*\*[[:space:]]*:?[[:space:]]*/, "", line)
                # Also strip leading colon if present.
                sub(/^:[[:space:]]*/, "", line)
                # Reject placeholder content (square brackets, "TBD", "TODO", or empty/whitespace).
                if (line == "" || line ~ /^\[[^]]*\]$/ || tolower(line) ~ /^(tbd|todo)$/) next
                # If line has any real non-bracket content, count it.
                # Simple heuristic: non-empty after stripping known placeholder shapes.
                gsub(/\[[^]]*\]/, "", line)
                gsub(/[[:space:]]+/, " ", line)
                if (length(line) > 3) populated=1
            }
            END {print populated}
        ' "$page")
        if [[ "$populated" != "1" ]]; then
            echo "  MISSING-DECISION-BASIS: ${page#$WIKI/}"
            found=1
        fi
    done < <(find "$decisions_dir" -type f -name '*.md')
    if [[ $found == 0 ]]; then echo "  ok"; else EXIT=1; fi
}

#------------------------------------------------------------------------------
# BLAST RADIUS — pages linking to a target page.
#------------------------------------------------------------------------------
check_blast() {
    if [[ -z "$BLAST_TARGET" ]]; then
        echo "lint.sh: --blast-radius requires a page name argument" >&2
        exit 2
    fi
    local stem
    stem=$(basename "$BLAST_TARGET" .md)
    echo "== blast-radius for $stem =="
    local found=0
    # Match [[stem]] OR [[anything/stem]] OR [[../anything/stem]].
    while IFS= read -r match; do
        [[ -z "$match" ]] && continue
        echo "  $match"
        found=1
    done < <(grep -rlE --exclude-dir=raw "\[\[([A-Za-z0-9_./-]+/)?${stem}\]\]" "$WIKI" 2>/dev/null | sed "s|^$WIKI/||" | sort)
    if [[ $found == 0 ]]; then echo "  (no inbound links)"; fi
}

#------------------------------------------------------------------------------
# Dispatch.
#------------------------------------------------------------------------------
case "$MODE" in
    drift) check_drift ;;
    orphans) check_orphans ;;
    stale) check_stale ;;
    duplicates) check_duplicates ;;
    missing-sources) check_missing_sources ;;
    missing-decision-basis) check_missing_decision_basis ;;
    blast) check_blast ;;
    all)
        check_drift
        check_orphans
        check_stale
        check_duplicates
        check_missing_sources
        check_missing_decision_basis
        ;;
esac

exit $EXIT
