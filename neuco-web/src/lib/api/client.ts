import { api } from './typed-client';

export class ApiError extends Error {
	constructor(
		public readonly status: number,
		public readonly statusText: string,
		public readonly body: unknown
	) {
		super(`API Error ${status}: ${statusText}`);
		this.name = 'ApiError';
	}
}

function snakeToCamel(str: string): string {
	return str.replace(/_([a-z0-9])/g, (_, letter) => letter.toUpperCase());
}

export function transformKeys(obj: unknown): unknown {
	if (obj === null || obj === undefined) return obj;
	if (Array.isArray(obj)) return obj.map(transformKeys);
	if (typeof obj === 'object') {
		const result: Record<string, unknown> = {};
		for (const [key, value] of Object.entries(obj as Record<string, unknown>)) {
			result[snakeToCamel(key)] = transformKeys(value);
		}
		return result;
	}
	return obj;
}

function camelToSnake(str: string): string {
	return str.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`);
}

export function transformKeysToSnake(obj: unknown): unknown {
	if (obj === null || obj === undefined) return obj;
	if (Array.isArray(obj)) return obj.map(transformKeysToSnake);
	if (typeof obj === 'object') {
		const result: Record<string, unknown> = {};
		for (const [key, value] of Object.entries(obj as Record<string, unknown>)) {
			result[camelToSnake(key)] = transformKeysToSnake(value);
		}
		return result;
	}
	return obj;
}

interface RequestOptions {
	headers?: Record<string, string>;
	signal?: AbortSignal;
}

async function request<T>(
	method: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE',
	path: string,
	body?: unknown,
	options: RequestOptions = {}
): Promise<T> {
	const requestOptions: Record<string, unknown> = {};

	if (options.signal) requestOptions.signal = options.signal;
	if (options.headers) requestOptions.headers = options.headers;
	if (body !== undefined) requestOptions.body = transformKeysToSnake(body);

	const response = await (api as any)[method](path, requestOptions);

	if (response.error) {
		throw new ApiError(
			response.response.status,
			response.response.statusText,
			transformKeys(response.error)
		);
	}

	if (response.response.status === 204) {
		return undefined as T;
	}

	return transformKeys(response.data) as T;
}

export const apiClient = {
	get<T>(path: string, options?: RequestOptions): Promise<T> {
		return request<T>('GET', path, undefined, options);
	},
	post<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
		return request<T>('POST', path, body, options);
	},
	patch<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
		return request<T>('PATCH', path, body, options);
	},
	put<T>(path: string, body?: unknown, options?: RequestOptions): Promise<T> {
		return request<T>('PUT', path, body, options);
	},
	delete<T>(path: string, options?: RequestOptions): Promise<T> {
		return request<T>('DELETE', path, undefined, options);
	}
};

export default apiClient;
