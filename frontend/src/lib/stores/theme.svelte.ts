let mode = $state<'light' | 'dark'>('dark');

export function getTheme() {
	return {
		get mode() {
			return mode;
		},

		init() {
			const stored = localStorage.getItem('hop_theme');
			if (stored === 'light' || stored === 'dark') {
				mode = stored;
			} else if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
				mode = 'dark';
			}
			applyMode();
		},

		toggle() {
			mode = mode === 'dark' ? 'light' : 'dark';
			localStorage.setItem('hop_theme', mode);
			applyMode();
		}
	};
}

function applyMode() {
	if (mode === 'dark') {
		document.documentElement.classList.add('dark');
	} else {
		document.documentElement.classList.remove('dark');
	}
}
