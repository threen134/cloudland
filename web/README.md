# CloudLand UI

CloudLand UI is a modern, Vue 3-based frontend for the CloudLand cloud resources management platform. It features a premium, RakSmart-inspired aesthetic with a robust feature set including multi-tenant support, multi-region management, and a comprehensive e-commerce marketplace for cloud services.

## Features

*   **Premium Design**: "Bold Blue" theme inspired by leading cloud providers (RakSmart, IBM Cloud), featuring glassmorphism, responsive layouts, and high-contrast typography.
*   **Internationalization (i18n)**: Full support for English and Simplified Chinese (`zh-CN`), with auto-detection and persistence.
*   **Multi-Tenancy**: Organization and tenant switching capabilities.
*   **Multi-Region**: Global region and zone selection strategy.
*   **Cloud Resources**: Management interfaces for Instances (VMs), Volumes (Block Storage), VPCs, Subnets, Floating IPs, and Load Balancers.
*   **Marketplace**: E-commerce functionality for browsing, configuring, and purchasing cloud resources.
*   **Mock API**: Built-in mock data for development and testing without a live backend.

## Tech Stack

*   **Framework**: [Vue 3](https://vuejs.org/) (Composition API, `<script setup>`)
*   **Build Tool**: [Vite](https://vitejs.dev/)
*   **State Management**: [Pinia](https://pinia.vuejs.org/)
*   **Routing**: [Vue Router 4](https://router.vuejs.org/)
*   **Internationalization**: [Vue I18n v10+](https://kazupon.github.io/vue-i18n/)
*   **Icons**: [Lucide Vue Next](https://lucide.dev/)
*   **Validation**: Zod (planned)
*   **Language**: TypeScript

## Prerequisites

*   **Node.js**: v18.0.0 or higher
*   **npm**: v9.0.0 or higher

## Getting Started

### 1. Clone the repository

```bash
git clone https://github.com/your-org/cloudland-ui.git
cd cloudland-ui
```

### 2. Install Dependencies

```bash
npm install
```

### 3. Start Development Server

```bash
npm run dev
```

The application will be available at `http://localhost:5173`.

### 4. Build for Production

```bash
npm run build
```

This will generate a production-ready build in the `dist` directory.

## Project Structure

```
src/
├── api/             # API client and service modules
├── assets/          # Static assets (images, fonts)
├── components/      # Reusable UI components
├── locales/         # i18n translation files (en.ts, zh.ts)
├── router/          # Vue Router configuration
├── stores/          # Pinia state stores (auth, cart, region, tenant)
├── styles/          # Global styles and variables
│   ├── main.css     # CSS entry point
│   └── variables.css # Design tokens (colors, spacing)
├── views/           # Page components
│   ├── dashboard/   # Authenticated dashboard views
│   └── marketplace/ # Public/Private marketplace views
├── App.vue          # Root component
└── main.ts          # Application entry point
```

## Configuration

### Environment Variables

Create a `.env` file in the root directory to configure the application (optional for mock mode):

```env
VITE_API_BASE_URL=http://localhost:8080/api/v1
```

### Mock Mode

The application currently uses a hybrid approach. It will attempt to call the backend API, but many components have fallback mock data if the API is unreachable or configured for testing.

## Contributing

1.  Create a feature branch: `git checkout -b feature/my-new-feature`
2.  Commit your changes: `git commit -m 'Add some feature'`
3.  Push to the branch: `git push origin feature/my-new-feature`
4.  Submit a pull request

## License

[MIT](LICENSE)
