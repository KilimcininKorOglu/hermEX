import js from '@eslint/js'
import globals from 'globals'
import tsParser from '@typescript-eslint/parser'
import tsPlugin from '@typescript-eslint/eslint-plugin'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import { defineConfig, globalIgnores } from 'eslint/config'

// Flat config for the webmail client. It mirrors the admin panel's intent
// (catch unused symbols and React-hooks misuse) but parses TypeScript/TSX with
// the typescript-eslint parser. The full typescript-eslint "recommended" preset
// is intentionally NOT enabled: it would flag many pre-existing patterns across
// the codebase and turn a "make lint runnable" task into a large cleanup.
export default defineConfig([
  globalIgnores(['dist', 'node_modules', '*.config.js', '*.config.ts']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [js.configs.recommended],
    languageOptions: {
      parser: tsParser,
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: { ...globals.browser },
      parserOptions: {
        ecmaFeatures: { jsx: true },
      },
    },
    plugins: {
      '@typescript-eslint': tsPlugin,
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      // TypeScript itself resolves identifiers (JSX runtime, lib types like
      // RequestInit), so the lexical no-undef rule only produces false positives.
      'no-undef': 'off',
      // The base rule misfires on TS types; use the typescript-eslint variant.
      'no-unused-vars': 'off',
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          varsIgnorePattern: '^[A-Z_]',
          argsIgnorePattern: '^_',
          caughtErrors: 'none',
        },
      ],
      'react-hooks/rules-of-hooks': 'error',
    },
  },
  {
    // Test files run under vitest/node globals.
    files: ['**/*.test.{ts,tsx}'],
    languageOptions: {
      globals: { ...globals.node, ...globals.browser },
    },
  },
])
