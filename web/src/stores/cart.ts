import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export interface CartItem {
    id: string
    productId: string
    name: string
    category: 'compute' | 'storage' | 'network'
    specs: {
        cpu?: string
        ram?: string
        storage?: string
        bandwidth?: string
    }
    price: number  // Monthly price in cents
    quantity: number
    billingCycle: 'hourly' | 'monthly' | 'yearly'
}

export const useCartStore = defineStore('cart', () => {
    const items = ref<CartItem[]>([])
    const isOpen = ref(false)

    // Total items count
    const itemCount = computed(() => {
        return items.value.reduce((sum, item) => sum + item.quantity, 0)
    })

    // Total price in cents
    const totalPrice = computed(() => {
        return items.value.reduce((sum, item) => {
            let multiplier = 1
            if (item.billingCycle === 'yearly') multiplier = 12 * 0.85 // 15% yearly discount
            return sum + (item.price * item.quantity * multiplier)
        }, 0)
    })

    // Formatted total price
    const formattedTotal = computed(() => {
        return `$${(totalPrice.value / 100).toFixed(2)}`
    })

    // Initialize from localStorage
    const init = () => {
        const storedCart = localStorage.getItem('cloudland_cart')
        if (storedCart) {
            try {
                items.value = JSON.parse(storedCart)
            } catch (e) {
                console.warn('Failed to parse cart from storage')
            }
        }
    }

    // Save cart to localStorage
    const saveCart = () => {
        localStorage.setItem('cloudland_cart', JSON.stringify(items.value))
    }

    // Add item to cart
    const addItem = (item: Omit<CartItem, 'id' | 'quantity'>) => {
        const existingItem = items.value.find(
            i => i.productId === item.productId && i.billingCycle === item.billingCycle
        )

        if (existingItem) {
            existingItem.quantity += 1
        } else {
            items.value.push({
                ...item,
                id: `cart-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
                quantity: 1
            })
        }

        saveCart()
    }

    // Remove item from cart
    const removeItem = (itemId: string) => {
        const index = items.value.findIndex(i => i.id === itemId)
        if (index !== -1) {
            items.value.splice(index, 1)
            saveCart()
        }
    }

    // Update item quantity
    const updateQuantity = (itemId: string, quantity: number) => {
        const item = items.value.find(i => i.id === itemId)
        if (item) {
            if (quantity <= 0) {
                removeItem(itemId)
            } else {
                item.quantity = quantity
                saveCart()
            }
        }
    }

    // Clear cart
    const clearCart = () => {
        items.value = []
        localStorage.removeItem('cloudland_cart')
    }

    // Toggle cart drawer
    const toggleCart = () => {
        isOpen.value = !isOpen.value
    }

    const openCart = () => {
        isOpen.value = true
    }

    const closeCart = () => {
        isOpen.value = false
    }

    // Initialize on store creation
    init()

    return {
        items,
        isOpen,
        itemCount,
        totalPrice,
        formattedTotal,
        addItem,
        removeItem,
        updateQuantity,
        clearCart,
        toggleCart,
        openCart,
        closeCart
    }
})
